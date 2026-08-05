#!/usr/bin/env bash
# cleanup-orphans.sh — tear down orphaned harness runs left behind when an upgrade
# test fails or is aborted mid-apply (e.g. SSO expired mid-run, so the harness's own
# deferred teardown couldn't authenticate).
#
# For each orphaned per-run state prefix (local-<ts>-<cfg>/...) it:
#   1. disables NLB deletion protection (v6.0 baselines create the NLB protected),
#   2. force-releases any held Terraform state lock,
#   3. runs `terragrunt destroy` on the physical layer (deleting the EKS cluster also
#      disposes of the in-cluster helm/k8s resources),
#   4. purges the run's state objects — ONLY if the destroy succeeded,
# then sweeps the addon-created containerinsights log groups (out-of-band, not TF
# managed) and runs verify-clean.sh to confirm the account is clean.
#
# Run from your terminal AFTER `aws sso login` — each destroy can take ~25 min.
#
# Usage:
#   ./cleanup-orphans.sh                       # auto-detect + tear down all local-* orphans
#   ./cleanup-orphans.sh local-1700000000-min_default-min_default   # a specific prefix
#   CUSTOMER=smoke ./cleanup-orphans.sh        # override the resource-name prefix (default: smoke)
#
# STATUS (round 5): harness/janitor.go is the primary implementation for the Argo path
# (the nightly janitor cron never shells out to this script). This script stays because
# run.sh's local-runner teardown (run.sh:144, "STARTED_RUN" trap) still calls it directly,
# and because three of its steps were deliberately NOT ported to Go:
#   - step 2b, the DR-bucket _harness/ object purge (touches object VERSIONS across every
#     matching bucket, a wider blast radius than the janitor's single-candidate scope;
#     tracked as a future round)
#   - step 6, central Vault auth-mount/policy cleanup (needs its own Vault credential/RBAC
#     story - a separate concern from the AWS-only janitor)
#   - step 7, verify-clean.sh (the janitor's own tag query already serves that role for
#     its own candidates)
# Everything else here - NLB deletion-protection clear, MSK cluster delete, lock/digest
# clear, EBS volume + launch template reclaim, lambda/DMS/containerinsights log-group
# sweep, the flux-source-controller IAM role cleanup - now also runs from Go inside
# harness/janitor.go's Sweep, gated the same way this script's DRY_RUN was SUPPOSED to
# gate every mutation (report mode there is enforced by which code path can reach a
# call, not a per-call flag, which closes two bugs this script still has: the DynamoDB
# lock/digest delete and the log-group delete both bypass DRY_RUN, and the lock scan
# below matches by substring instead of an exact key).
set -uo pipefail
cd "$(dirname "$0")"
HARNESS_DIR="$PWD"
MARKERS_DIR="$HARNESS_DIR/__worktrees__/.markers"
LIVE_ROOT="$(cd .. && pwd)"

CUSTOMER="${CUSTOMER:-smoke}"
P="${AWS_PROFILE:-default}"
R="${AWS_REGION:-us-east-1}"
DR="${DR_REGION:-us-west-2}"
# Drop stale static creds so AWS_PROFILE (SSO) is authoritative.
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
export AWS_PROFILE="$P" TG_TF_PATH="${TG_TF_PATH:-${TERRAGRUNT_TFPATH:-tofu}}"
unset TERRAGRUNT_TFPATH

ACCT="$(aws sts get-caller-identity --profile "$P" --query Account --output text 2>/dev/null)" || {
  echo "ERROR: no AWS identity (profile=$P). Run: aws sso login --profile $P" >&2; exit 1; }
# State lives in the bucket of the region the stack was deployed to, so the recover
# scenario writes its REBUILD stack's state to the DR region's bucket while the source
# stack's stays in the primary. Scanning only the primary made that rebuild stack
# invisible here: cleanup found just the source, whose destroy is BLOCKED by the rebuild
# stack (it owns the S3 replication config on the source's DR buckets), so the backstop
# retried a doomed destroy while a live EKS + Aurora sat unseen in us-west-2.
PRIMARY_BUCKET="dozuki-terraform-state-${R}-${ACCT}"
DR_BUCKET="dozuki-terraform-state-${DR}-${ACCT}"
if [ "$DR_BUCKET" = "$PRIMARY_BUCKET" ]; then STATE_BUCKETS="$PRIMARY_BUCKET"; else STATE_BUCKETS="$PRIMARY_BUCKET $DR_BUCKET"; fi
BUCKET="$PRIMARY_BUCKET"   # kept for messages that predate the multi-bucket scan
LOCK_TABLE="dozuki-terraform-lock"

# Number of managed CLOUD resource instances a run's state still tracks, summed over its
# state files. Two kinds of entry are excluded, for different reasons:
#
#   data sources - re-read on every plan, own nothing.
#   in-cluster resources (kubernetes_*, helm_*, vault_*) - these live inside the EKS
#     cluster this same state built. Once the cluster is gone they refer to nothing and
#     cannot be destroyed even in principle; a stranded kubernetes_namespace_v1 is not a
#     reason to keep a dead state forever. verify-clean scans the account for the cluster
#     itself, so a surviving cluster is caught there rather than inferred from here.
#
# What is left is the cloud footprint - the things that actually cost money and hold
# quota. Zero of those means the stack is genuinely gone.
#
# Prints -1 if any state file cannot be read or parsed, which callers must treat as "might
# still manage something" — this decides whether state is safe to delete, so an unreadable
# file has to fail closed.
managed_resource_count() {
  local bkt="$1" prefix="$2" total=0 n k
  for k in $(aws s3 ls "s3://$bkt/$prefix/" --recursive --profile "$P" 2>/dev/null \
             | awk '$4 ~ /terraform\.tfstate$/ {print $4}'); do
    n=$(aws s3 cp "s3://$bkt/$k" - --profile "$P" 2>/dev/null | python3 -c '
import json, sys
CLOUD = ("aws_", "azurerm_")
try:
    d = json.load(sys.stdin)
except Exception:
    print(-1); raise SystemExit
print(sum(1 for r in d.get("resources", [])
          if r.get("mode") == "managed"
          and r.get("instances")
          and str(r.get("type", "")).startswith(CLOUD)))' 2>/dev/null)
    [ -z "$n" ] && n=-1
    if [ "$n" -lt 0 ]; then echo -1; return; fi
    total=$((total + n))
  done
  echo "$total"
}

# The recovery scenario's rebuild stack is configured with RUNTIME terraform inputs
# (snapshot ARN, adopted DR buckets, the Vault PrivateLink service name). They are not in
# env.hcl and not derivable from live outputs, so a destroy without them dies at variable
# validation - "No value for required variable vault_endpoint_service_name" - and the
# stack cannot be torn down by this script at all. That is exactly what stranded a live
# EKS + Aurora in us-west-2 until the values were dug out of the state file by hand.
#
# The harness persists them in the run manifest as extra_inputs. The manifest is written
# by the PRIMARY-region store even when the stack's state lives in the DR bucket, so look
# in every state bucket for it and use the first one that has it.
# Applies manifest_tfvars output to the current shell. Deliberately a function with an
# explicit empty guard: `eval "$(printf '' | sed 's/^/export /')"` evaluates to a BARE
# `export`, which prints the entire environment - tokens included - into the run log.
apply_manifest_tfvars() {
  [ -n "${MANIFEST_TFVARS:-}" ] || return 0
  eval "$(printf '%s\n' "$MANIFEST_TFVARS" | sed 's/^/export /')"
}

manifest_tfvars() { # <prefix> — prints TF_VAR_<k>=<v> lines, one per extra input
  local prefix="$1" b json
  for b in $STATE_BUCKETS; do
    json="$(aws s3 cp "s3://$b/$prefix/harness-manifest.json" - --profile "$P" 2>/dev/null)" || continue
    [ -z "$json" ] && continue
    printf '%s' "$json" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for k, v in (d.get("extra_inputs") or {}).items():
    # HCL-ish scalars pass straight through; anything structured goes as compact JSON,
    # which is what terraform expects for list/map/object vars via TF_VAR_.
    print("TF_VAR_%s=%s" % (k, v if isinstance(v, str) else json.dumps(v, separators=(",", ":"))))
' && return 0
  done
  return 0
}

# Target prefixes, filtered to those that START WITH an arg — so a full prefix matches
# itself, and a RUN_ID like "local-<ts>-" matches all of that run's per-config prefixes
# (and nothing else).
#
# With NO args we only ever consider local-* (run.sh's default RUN_ID shape), so a blind
# sweep can never touch a real stack's state in this bucket. With an EXPLICIT arg we match
# any prefix: run.sh lets RUN_ID be overridden, and a custom RUN_ID used to match nothing
# here — the destroy silently no-op'd and the whole run leaked (EKS, Aurora, VPCs) while
# still reporting that cleanup had run.
ALL_PREFIXES=""
for _b in $STATE_BUCKETS; do
  if [ "$#" -gt 0 ]; then
    _p="$(aws s3 ls "s3://$_b/" --profile "$P" 2>/dev/null | awk '/PRE /{gsub(/\//,"",$2); print $2}')"
  else
    _p="$(aws s3 ls "s3://$_b/" --profile "$P" 2>/dev/null | awk '/PRE local-/{gsub(/\//,"",$2); print $2}')"
  fi
  # bucket<TAB>prefix, so the loop destroys each stack against the bucket holding its state
  ALL_PREFIXES="$ALL_PREFIXES
$(printf '%s\n' "$_p" | awk -v b="$_b" 'NF{print b "\t" $0}')"
done
ALL_PREFIXES="$(printf '%s\n' "$ALL_PREFIXES" | awk 'NF')"
if [ "$#" -gt 0 ]; then
  PREFIXES=""
  for arg in "$@"; do
    arg="${arg%/}"
    PREFIXES="$PREFIXES
$(printf '%s\n' "$ALL_PREFIXES" | awk -F'\t' -v a="$arg" 'index($2,a)==1')"
  done
  PREFIXES="$(printf '%s\n' "$PREFIXES" | awk 'NF' | sort -u)"
else
  PREFIXES="$ALL_PREFIXES"
fi
if [ -z "$PREFIXES" ]; then echo ">> No matching orphaned state prefixes found in $STATE_BUCKETS (searched: ${*:-local-*})."; fi

fail=0
STACKS=""   # collected <customer>-<env> stacks, for central-Vault cleanup after teardown
# Loop in the parent shell (process substitution, not a pipe) so set/exit behave.
while IFS="$(printf '\t')" read -r bkt pfx; do
  [ -z "$pfx" ] && continue
  echo; echo "==================== orphan: $pfx ===================="

  # Derive partition/region/env from the physical state key path.
  key="$(aws s3 ls "s3://$bkt/$pfx/" --recursive --profile "$P" 2>/dev/null | awk '/physical\/terraform.tfstate$/{print $4; exit}')"
  if [ -n "$key" ]; then
    rel="${key#"$pfx"/}"                       # standard/us-east-1/min/physical/terraform.tfstate
    envdir="$(dirname "$(dirname "$rel")")"    # standard/us-east-1/min
    region="$(printf '%s' "$envdir" | awk -F/ '{print $2}')"; region="${region:-$R}"
    env="$(printf '%s' "$envdir" | awk -F/ '{print $3}')"
    # Derive the customer from the stack's OWN state (eks_cluster_id output is
    # "<customer>-<env>"). CUSTOMER is only the fallback: recovery-scenario configs
    # run other customers (smokesrc/smokerec), and assuming 'smoke' once made the
    # vault cleanup disable the WRONG mount and verify-clean vouch for the wrong
    # prefix while real orphans sat in the account.
    cust="$(aws s3 cp "s3://$bkt/$key" - --profile "$P" 2>/dev/null | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
    print((d.get("outputs", {}).get("eks_cluster_id", {}) or {}).get("value", ""))
except Exception:
    print("")' 2>/dev/null)"
    cust="${cust%-"$env"}"
    [ -n "$cust" ] || cust="$CUSTOMER"
    [ -n "$env" ] && STACKS="$STACKS ${cust}-${env}"
    echo "  partition path: $envdir  (region=$region env=$env customer=$cust)"
  else
    echo "  no physical state under this prefix — will release locks + purge state only"
    envdir=""; region="$R"; env=""; cust="$CUSTOMER"
  fi

  # 1) Disable NLB deletion protection (<customer>-<env>).
  if [ -n "$env" ]; then
    arn="$(aws elbv2 describe-load-balancers --region "$region" --profile "$P" \
          --query "LoadBalancers[?LoadBalancerName=='${cust}-${env}'].LoadBalancerArn|[0]" --output text 2>/dev/null)"
    if [ -n "$arn" ] && [ "$arn" != "None" ]; then
      aws elbv2 modify-load-balancer-attributes --load-balancer-arn "$arn" \
        --attributes Key=deletion_protection.enabled,Value=false --region "$region" --profile "$P" >/dev/null 2>&1 \
        && echo "  NLB deletion protection disabled (${cust}-${env})"
    fi
  fi

  # 1b) Delete any MSK cluster for this stack that Terraform does not know about.
  # A run killed mid-apply (SIGKILL, so no trap) can finish creating MSK after the
  # state was last written, leaving an ACTIVE cluster absent from state. `destroy`
  # then removes the MSK *configuration* but never the cluster, and the cluster's
  # ENIs pin every subnet and security group in the VPC — so the VPC destroy fails
  # forever and the whole network is stranded. Delete by name and let the retry loop
  # below wait it out. Idempotent: no cluster, no output.
  if [ -n "$env" ]; then
    for carn in $(aws kafka list-clusters --region "$region" --profile "$P" \
          --query "ClusterInfoList[?starts_with(ClusterName,'${cust}-${env}')].ClusterArn" --output text 2>/dev/null); do
      [ -z "$carn" ] || [ "$carn" = "None" ] && continue
      aws kafka delete-cluster --cluster-arn "$carn" --region "$region" --profile "$P" >/dev/null 2>&1 \
        && echo "  MSK cluster delete requested: $carn"
    done
  fi

  # 2) Force-release held state locks AND clear the -md5 state digest for this prefix.
  #    The digest item (LockID "<key>-md5") is the S3 backend's consistency check. An
  #    interrupted apply leaves it out of sync with the S3 object, so a later destroy
  #    aborts with "state data in S3 does not have the expected content" and strands
  #    the infra. We are tearing down (state integrity is moot), so drop BOTH the lock
  #    and the digest — terragrunt then reads the actual S3 state and writes a fresh
  #    digest. (No -md5 filter here, unlike a normal lock-release.)
  for lk in $(aws dynamodb scan --table-name "$LOCK_TABLE" --region "$R" --profile "$P" \
        --query "Items[?contains(LockID.S, '$pfx')].LockID.S" --output text 2>/dev/null); do
    aws dynamodb delete-item --table-name "$LOCK_TABLE" --region "$R" --profile "$P" \
      --key "{\"LockID\":{\"S\":\"$lk\"}}" >/dev/null 2>&1 && echo "  cleared lock/digest: $lk"
  done

  # 2b) Drop the harness's own objects from the DR replica buckets before destroying.
  #
  # The DR buckets are created WITHOUT force_destroy (physical/dr.tf), unlike their source
  # counterparts, so terraform cannot delete one that holds anything:
  #
  #   deleting S3 Bucket (…-image-dr-…): api error BucketNotEmpty
  #
  # and the whole destroy dies there, stranding the DR buckets plus everything it had not
  # reached yet. The harness reliably puts objects there: the DR replication canary, and on
  # upgrade runs the continuity sentinel, both of which replicate from source by design.
  #
  # Scoped to the _harness/ prefix and to this run's buckets, and deletes by VERSION -
  # these buckets are versioned, so a plain delete would only add a delete marker and leave
  # the bucket non-empty. The proper fix is force_destroy on the DR buckets; this keeps
  # teardown working on stacks pinned to a ref that predates it.
  for _reg in "$R" "${DR_REGION:-us-west-2}"; do
    for _b in $(aws s3api list-buckets --profile "$P" \
                  --query "Buckets[?starts_with(Name, '${cust}-')].Name" --output text 2>/dev/null); do
      aws s3api list-object-versions --bucket "$_b" --region "$_reg" --prefix "_harness/" \
          --profile "$P" --output json 2>/dev/null \
        | python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit()
o=[{'Key':x['Key'],'VersionId':x['VersionId']} for k in ('Versions','DeleteMarkers') for x in d.get(k) or []]
print(json.dumps({'Objects':o}) if o else '', end='')
" > /tmp/.cleanup-harness-objs.$$ 2>/dev/null
      if [ -s /tmp/.cleanup-harness-objs.$$ ]; then
        aws s3api delete-objects --bucket "$_b" --region "$_reg" --profile "$P" \
          --delete "file:///tmp/.cleanup-harness-objs.$$" >/dev/null 2>&1 \
          && echo "  purged _harness/ objects from $_b ($_reg)"
      fi
      rm -f /tmp/.cleanup-harness-objs.$$
    done
  done

  # 3) Destroy against the worktree whose code matches the deployed state (recorded
  #    by the harness in a marker). The live tree is the current branch's code, which
  #    does NOT match for cross-architecture upgrades — use it only as a last resort.
  destroyed_ok=1
  marker="$MARKERS_DIR/$(printf '%s' "$pfx" | tr '/' '_')"
  tgt=""
  if [ -f "$marker" ] && [ -d "$(cat "$marker")/physical" ]; then
    tgt="$(cat "$marker")"
    echo "  destroy target: worktree $tgt (from marker)"
  elif [ -n "$key" ] && [ -d "$LIVE_ROOT/$envdir/physical" ]; then
    tgt="$LIVE_ROOT/$envdir"
    # Two very different situations reach here, and saying "no marker" for both trains
    # everyone to skim past this line. A marker whose worktree is GONE means the run's
    # own teardown already finished and removed it, so there is usually nothing left to
    # destroy (the state-resource count below confirms it). A genuinely missing marker
    # means the harness never recorded the deployed code, which is the case that leaks
    # a whole stack. Note the live tree is NOT self-contained: terragrunt fails to load
    # it (find_in_parent_folders), so this fallback mostly produces noise either way.
    if [ -f "$marker" ]; then
      echo "  NOTE: marker for $pfx points at a removed worktree ($(cat "$marker")) — its teardown already ran; trying LIVE tree $tgt" >&2
    else
      echo "  WARNING: no worktree marker for $pfx — falling back to LIVE tree $tgt (may not match deployed code)" >&2
    fi
  fi
  # Runtime inputs the rebuild stack needs to even parse its variables.
  MANIFEST_TFVARS="$(manifest_tfvars "$pfx")"
  if [ -n "$MANIFEST_TFVARS" ]; then
    echo "  manifest extra_inputs: $(printf '%s\n' "$MANIFEST_TFVARS" | awk -F= '{printf "%s ", $1}')"
  fi
  if [ -n "$tgt" ]; then
    if [ "${DRY_RUN:-0}" = 1 ]; then
      echo "  DRY_RUN: would destroy logical (best-effort) then physical in $tgt"; destroyed_ok=0
    else
      ( cd "$tgt/logical" 2>/dev/null && rm -rf .terragrunt-cache && \
        apply_manifest_tfvars && \
        TG_AWS_ACCT_ID="$ACCT" TG_AWS_PROFILE="$P" TG_AWS_REGION="$region" TG_STATE_PREFIX="$pfx/" \
        TF_VAR_customer="$cust" TF_VAR_enable_dr=false \
          terragrunt destroy --non-interactive -auto-approve -input=false ) \
        || echo "  logical destroy failed (continuing to physical so infra isn't stranded)" >&2
      # Physical destroy, retried. A single pass routinely fails on transient
      # DependencyViolation: a killed run can leave MSK/EKS/NAT still creating or
      # deleting, and their ENIs pin the subnets and security groups until AWS
      # finishes releasing them. Those errors clear on their own given time, so
      # retry with a pause rather than declaring the stack un-destroyable and
      # leaving a VPC behind. Attempts/backoff overridable for a quick sweep.
      DESTROY_ATTEMPTS="${DESTROY_ATTEMPTS:-4}"
      DESTROY_BACKOFF="${DESTROY_BACKOFF:-120}"
      destroyed_ok=1
      _dlog="$(mktemp -t cleanup-destroy.XXXXXX)"
      for attempt in $(seq 1 "$DESTROY_ATTEMPTS"); do
        ( cd "$tgt/physical"
          rm -rf .terragrunt-cache
          apply_manifest_tfvars
          TG_AWS_ACCT_ID="$ACCT" TG_AWS_PROFILE="$P" TG_AWS_REGION="$region" TG_STATE_PREFIX="$pfx/" \
          TF_VAR_customer="$cust" TF_VAR_enable_dr=false \
            terragrunt destroy --non-interactive -auto-approve -input=false ) 2>&1 | tee "$_dlog"
        destroyed_ok=${PIPESTATUS[0]}
        [ "$destroyed_ok" -eq 0 ] && break
        # Only retry things that can actually clear on their own. A config-resolution
        # failure (e.g. falling back to the LIVE tree, where find_in_parent_folders has
        # no terragrunt.hcl to find) fails identically every time, so retrying burns
        # DESTROY_ATTEMPTS * DESTROY_BACKOFF for nothing — 8 minutes per pass, observed.
        if grep -qE "find_in_parent_folders|ParentFileNotFound|Could not find a terragrunt" "$_dlog" 2>/dev/null; then
          echo "  physical destroy failed to even load a config (no usable tree for $pfx) — not retrying" >&2
          break
        fi
        if [ "$attempt" -lt "$DESTROY_ATTEMPTS" ]; then
          echo "  physical destroy attempt $attempt/$DESTROY_ATTEMPTS failed — waiting ${DESTROY_BACKOFF}s for in-flight deletions to drain, then retrying" >&2
          sleep "$DESTROY_BACKOFF"
        fi
      done
    fi
  elif [ -n "$key" ]; then
    echo "  WARNING: no worktree marker and no live $LIVE_ROOT/$envdir/physical — cannot destroy via terragrunt; leaving state intact." >&2
    destroyed_ok=1
  fi

  # 4) Purge state objects ONLY if the destroy succeeded (else keep state so it can retry).
  #
  # "Keep it for retry" is right when a destroy failed on something transient, but wrong
  # when the destroy could not run at all. Once a run's worktree is gone the fallback is
  # the LIVE tree, where find_in_parent_folders has no terragrunt.hcl to find, so the
  # destroy fails identically forever. The prefix is then kept forever, verify-clean
  # reports it as a leak forever, and no future cycle can ever start — a deadlock that
  # has to be broken by hand (observed: cycle 14 refused to start against an account
  # whose only "leak" was the dead state of an already-destroyed stack).
  #
  # Ask terraform instead of guessing: a state whose entries are all data sources manages
  # nothing, so there is nothing left to destroy and nothing a retry could accomplish.
  # Anything unreadable counts as "might still manage something" and is kept.
  if [ "${DRY_RUN:-0}" = 1 ]; then
    echo "  DRY_RUN: would purge state prefix $pfx (only if the real destroy succeeded)"
  elif [ "$destroyed_ok" -eq 0 ] || [ -z "$key" ]; then
    aws s3 rm "s3://$bkt/$pfx/" --recursive --profile "$P" >/dev/null 2>&1 && echo "  purged state prefix: $pfx"
  elif [ "$(managed_resource_count "$bkt" "$pfx")" = 0 ]; then
    echo "  destroy could not run, but the state manages 0 resources (data sources only) — purging dead prefix: $pfx" >&2
    aws s3 rm "s3://$bkt/$pfx/" --recursive --profile "$P" >/dev/null 2>&1 && echo "  purged state prefix: $pfx"
  else
    echo "  destroy did NOT fully succeed — state prefix kept for retry: $pfx" >&2
    fail=1
  fi

  # 4b) Reclaim resources terraform does not own: the app's dynamic PVCs create EBS
  #     volumes via the CSI driver (not in TF state), so destroy never removes them —
  #     they orphan as `available`. Also sweep orphaned launch templates. Detached/
  #     unused only; reports every deletion (no silent caps).
  if [ -n "$env" ]; then
    # cust, not CUSTOMER: recovery-scenario stacks run smokesrc/smokerec, so the default
    # would sweep smoke-min-* and silently leave the real stack's volumes behind.
    stack="${cust}-${env}"
    for vol in $(aws ec2 describe-volumes --region "$region" --profile "$P" \
          --filters "Name=tag:Name,Values=${stack}-dynamic-pvc-*" "Name=status,Values=available" \
          --query 'Volumes[].VolumeId' --output text 2>/dev/null | tr '\t' '\n'); do
      [ -z "$vol" ] && continue
      if [ "${DRY_RUN:-0}" = 1 ]; then echo "  DRY_RUN: would delete orphan volume $vol"; continue; fi
      aws ec2 delete-volume --region "$region" --profile "$P" --volume-id "$vol" >/dev/null 2>&1 \
        && echo "  reclaimed orphan EBS volume: $vol" || echo "  WARNING: could not delete volume $vol" >&2
    done
    for lt in $(aws ec2 describe-launch-templates --region "$region" --profile "$P" \
          --filters "Name=launch-template-name,Values=${stack}-*" \
          --query 'LaunchTemplates[].LaunchTemplateId' --output text 2>/dev/null | tr '\t' '\n'); do
      [ -z "$lt" ] && continue
      if [ "${DRY_RUN:-0}" = 1 ]; then echo "  DRY_RUN: would delete launch template $lt"; continue; fi
      aws ec2 delete-launch-template --region "$region" --profile "$P" --launch-template-id "$lt" >/dev/null 2>&1 \
        && echo "  reclaimed orphan launch template: $lt"
    done
  fi
done <<EOF
$PREFIXES
EOF

# 5) Sweep service-created log groups (out-of-band; TF doesn't own them, so a destroy
# leaves them behind). Two families: the containerinsights groups the CloudWatch addon
# creates, and /aws/lambda/<identifier>-* which Lambda creates on first invocation —
# the latter outlived every teardown and was the last straggler on each run.
# Every customer identity the matrix can deploy, not just the ones this run left state
# for. When a run's state is already purged (a clean teardown, or a prefix purged as
# empty) STACKS is empty and CUSTOMERS collapses to the default "smoke" - and because
# verify-clean.sh appends a hyphen, a "smoke-" sweep does NOT match smokesrc-/smokerec-
# and happily reports CLEAN with those still standing. That exact false-clean sent a
# later run into EntityAlreadyExists on a leftover IAM role. Reading the matrix keeps
# this correct as configs are added.
MATRIX_CUSTOMERS="$(sed 's/#.*//' "$HARNESS_DIR/matrix.yaml" | awk -F'"' '/customer:/{print $2}' | awk 'NF' | sort -u)"
CUSTOMERS="$(printf '%s\n' "$CUSTOMER" $MATRIX_CUSTOMERS $(printf '%s\n' $STACKS | sed 's/-[^-]*$//') | awk 'NF' | sort -u)"
for c in $CUSTOMERS; do
for prefix in "/aws/containerinsights/${c}-" "/aws/lambda/${c}-" "dms-tasks-${c}-"; do
  for lg in $(aws logs describe-log-groups --region "$R" --profile "$P" \
        --query "logGroups[?starts_with(logGroupName,'${prefix}')].logGroupName" --output text 2>/dev/null); do
    aws logs delete-log-group --log-group-name "$lg" --region "$R" --profile "$P" 2>/dev/null && echo "  deleted log group: $lg"
  done
done
done

# 6) Central Vault cleanup: disable each stack's k8s auth mount + delete its policy.
# The mount k8s/<customer>-<env> lives in the central Vault (account 0106), keyed by
# stack name, and is NOT torn down with the AWS stack. A leftover mount makes the next
# run's `vault_auth_backend` create fail ("path already in use"), so kubernetes_host
# stays pointed at the old cluster -> ESO gets 403 -> pods can't mount their secret ->
# helm hangs. Reuses an inherited VAULT_ADDR/VAULT_TOKEN (e.g. from run.sh's trap) or
# brings up its own tunnel + AWS-auth login. Skip with SKIP_VAULT_CLEANUP=1.
STACKS="$(printf '%s\n' $STACKS | awk 'NF' | sort -u)"
if [ -n "$STACKS" ] && [ "${SKIP_VAULT_CLEANUP:-0}" != 1 ]; then
  echo; echo "==================== central-Vault cleanup ===================="
  VPF=""
  VCTX="${VAULT_KUBE_CONTEXT:-vault-standard}"; VPROF="${VAULT_AWS_PROFILE:-dozuki}"; VROLE="${VAULT_AWS_ROLE:-admin}"

  # A token minted at run start (e.g. run.sh, up to ~1h ago) can EXPIRE during the run,
  # so never trust an inherited VAULT_TOKEN blindly — verify it with a real call and
  # re-authenticate if it's stale or missing. (The old code only re-authed when the
  # token was unset, so a long run's expired token was reused -> `vault auth disable`
  # failed silently -> the k8s/<stack> mount leaked into the next run.)
  vault_token_ok() { [ -n "${VAULT_ADDR:-}" ] && [ -n "${VAULT_TOKEN:-}" ] && vault token lookup >/dev/null 2>&1; }

  if ! vault_token_ok; then
    if command -v vault >/dev/null 2>&1 && command -v kubectl >/dev/null 2>&1 && command -v python3 >/dev/null 2>&1 \
       && aws sts get-caller-identity --profile "$VPROF" >/dev/null 2>&1; then
      kubectl --context "$VCTX" port-forward -n vault svc/vault-active 8204:8200 >/tmp/cleanup-vpf.log 2>&1 &
      VPF=$!; sleep 4
      export VAULT_ADDR="http://127.0.0.1:8204"
      VAULT_TOKEN="$( eval "$(aws --profile "$VPROF" configure export-credentials --format env 2>/dev/null)"; \
        vault login -method=aws role="$VROLE" -format=json 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)["auth"]["client_token"])' 2>/dev/null )"
      export VAULT_TOKEN
    fi
  fi

  if vault_token_ok; then
    for stack in $STACKS; do
      derr="$(vault auth disable "k8s/$stack" 2>&1)"; rc=$?
      if [ "$rc" -ne 0 ]; then
        echo "  WARNING: 'vault auth disable k8s/$stack' failed: $derr" >&2; fail=1
      elif vault auth list 2>/dev/null | grep -q "^k8s/$stack/"; then
        echo "  WARNING: k8s/$stack STILL present after disable — next run will hit 'path already in use'" >&2; fail=1
      else
        echo "  vault: disabled auth mount k8s/$stack"
      fi
      vault policy delete "$stack" >/dev/null 2>&1 || true
    done
  else
    echo "  WARNING: no working Vault token (inherited token expired and re-auth failed — is the '$VPROF' SSO session alive?)." >&2
    echo "           k8s/<stack> NOT disabled for: $STACKS -> the next run will fail on 'path already in use'." >&2
    echo "           Fix: aws sso login --profile $VPROF, then re-run ./cleanup-orphans.sh; or SKIP_VAULT_CLEANUP=1 to bypass." >&2
    fail=1
  fi
  [ -n "$VPF" ] && kill "$VPF" 2>/dev/null || true
fi

# 6b) Deterministically-named LOGICAL-layer IAM roles. The logical destroy is
# best-effort; when it fails, deleting the EKS cluster disposes of the in-cluster
# resources but NOT the IAM roles logical created. flux-source-controller has a fixed
# name, so the leftover collides with the next run's CreateRole (EntityAlreadyExists).
for stack in $STACKS; do
  role="${stack}-flux-source-controller"
  if aws iam get-role --role-name "$role" --profile "$P" >/dev/null 2>&1; then
    if [ "${DRY_RUN:-0}" = 1 ]; then echo "  DRY_RUN: would delete IAM role $role"; continue; fi
    for pa in $(aws iam list-attached-role-policies --role-name "$role" --profile "$P"           --query 'AttachedPolicies[].PolicyArn' --output text 2>/dev/null); do
      aws iam detach-role-policy --role-name "$role" --policy-arn "$pa" --profile "$P" 2>/dev/null
    done
    for pi in $(aws iam list-role-policies --role-name "$role" --profile "$P"           --query 'PolicyNames[]' --output text 2>/dev/null); do
      aws iam delete-role-policy --role-name "$role" --policy-name "$pi" --profile "$P" 2>/dev/null
    done
    aws iam delete-role --role-name "$role" --profile "$P" 2>/dev/null       && echo "  deleted orphan logical IAM role: $role"       || { echo "  WARNING: could not delete IAM role $role" >&2; fail=1; }
  fi
done

# 7) Verify — once per distinct customer seen (plus the default).
echo; echo "==================== verify-clean ===================="
for c in ${CUSTOMERS:-$CUSTOMER}; do
  DR_REGION="$DR" AWS_PROFILE="$P" "$HARNESS_DIR/verify-clean.sh" "$c" || fail=1
done

echo
if [ "$fail" -eq 0 ]; then echo "CLEANUP COMPLETE — account is clean."; else
  echo "CLEANUP INCOMPLETE — see warnings above (re-run after resolving)."; exit 1; fi
