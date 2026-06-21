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
export AWS_PROFILE="$P" TERRAGRUNT_TFPATH="${TERRAGRUNT_TFPATH:-tofu}"

ACCT="$(aws sts get-caller-identity --profile "$P" --query Account --output text 2>/dev/null)" || {
  echo "ERROR: no AWS identity (profile=$P). Run: aws sso login --profile $P" >&2; exit 1; }
BUCKET="dozuki-terraform-state-${R}-${ACCT}"
LOCK_TABLE="dozuki-terraform-lock"

# Target prefixes: every local-* prefix in the state bucket, optionally filtered to
# those that START WITH an arg — so a full prefix matches itself, and a RUN_ID like
# "local-<ts>-" matches all of that run's per-config prefixes (and nothing else).
ALL_PREFIXES="$(aws s3 ls "s3://$BUCKET/" --profile "$P" 2>/dev/null | awk '/PRE local-/{gsub(/\//,"",$2); print $2}')"
if [ "$#" -gt 0 ]; then
  PREFIXES=""
  for arg in "$@"; do
    arg="${arg%/}"
    PREFIXES="$PREFIXES
$(printf '%s\n' "$ALL_PREFIXES" | awk -v a="$arg" 'index($0,a)==1')"
  done
  PREFIXES="$(printf '%s\n' "$PREFIXES" | awk 'NF' | sort -u)"
else
  PREFIXES="$ALL_PREFIXES"
fi
if [ -z "$PREFIXES" ]; then echo ">> No matching orphaned local-* state prefixes found in s3://$BUCKET/."; fi

fail=0
STACKS=""   # collected <customer>-<env> stacks, for central-Vault cleanup after teardown
# Loop in the parent shell (process substitution, not a pipe) so set/exit behave.
while IFS= read -r pfx; do
  [ -z "$pfx" ] && continue
  echo; echo "==================== orphan: $pfx ===================="

  # Derive partition/region/env from the physical state key path.
  key="$(aws s3 ls "s3://$BUCKET/$pfx/" --recursive --profile "$P" 2>/dev/null | awk '/physical\/terraform.tfstate$/{print $4; exit}')"
  if [ -n "$key" ]; then
    rel="${key#"$pfx"/}"                       # standard/us-east-1/min/physical/terraform.tfstate
    envdir="$(dirname "$(dirname "$rel")")"    # standard/us-east-1/min
    region="$(printf '%s' "$envdir" | awk -F/ '{print $2}')"; region="${region:-$R}"
    env="$(printf '%s' "$envdir" | awk -F/ '{print $3}')"
    [ -n "$env" ] && STACKS="$STACKS ${CUSTOMER}-${env}"
    echo "  partition path: $envdir  (region=$region env=$env customer=$CUSTOMER)"
  else
    echo "  no physical state under this prefix — will release locks + purge state only"
    envdir=""; region="$R"; env=""
  fi

  # 1) Disable NLB deletion protection (<customer>-<env>).
  if [ -n "$env" ]; then
    arn="$(aws elbv2 describe-load-balancers --region "$region" --profile "$P" \
          --query "LoadBalancers[?LoadBalancerName=='${CUSTOMER}-${env}'].LoadBalancerArn|[0]" --output text 2>/dev/null)"
    if [ -n "$arn" ] && [ "$arn" != "None" ]; then
      aws elbv2 modify-load-balancer-attributes --load-balancer-arn "$arn" \
        --attributes Key=deletion_protection.enabled,Value=false --region "$region" --profile "$P" >/dev/null 2>&1 \
        && echo "  NLB deletion protection disabled (${CUSTOMER}-${env})"
    fi
  fi

  # 2) Force-release any held state locks for this prefix.
  for lk in $(aws dynamodb scan --table-name "$LOCK_TABLE" --region "$R" --profile "$P" \
        --query "Items[?contains(LockID.S, '$pfx') && !contains(LockID.S, '-md5')].LockID.S" --output text 2>/dev/null); do
    aws dynamodb delete-item --table-name "$LOCK_TABLE" --region "$R" --profile "$P" \
      --key "{\"LockID\":{\"S\":\"$lk\"}}" >/dev/null 2>&1 && echo "  released lock: $lk"
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
    echo "  WARNING: no worktree marker for $pfx — falling back to LIVE tree $tgt (may not match deployed code)" >&2
  fi
  if [ -n "$tgt" ]; then
    if [ "${DRY_RUN:-0}" = 1 ]; then
      echo "  DRY_RUN: would destroy logical (best-effort) then physical in $tgt"; destroyed_ok=0
    else
      ( cd "$tgt/logical" 2>/dev/null && rm -rf .terragrunt-cache && \
        TG_AWS_ACCT_ID="$ACCT" TG_AWS_PROFILE="$P" TG_AWS_REGION="$region" TG_STATE_PREFIX="$pfx/" \
        TF_VAR_customer="$CUSTOMER" TF_VAR_enable_dr=false \
          terragrunt destroy --terragrunt-non-interactive -auto-approve -input=false ) \
        || echo "  logical destroy failed (continuing to physical so infra isn't stranded)" >&2
      ( cd "$tgt/physical"
        rm -rf .terragrunt-cache
        TG_AWS_ACCT_ID="$ACCT" TG_AWS_PROFILE="$P" TG_AWS_REGION="$region" TG_STATE_PREFIX="$pfx/" \
        TF_VAR_customer="$CUSTOMER" TF_VAR_enable_dr=false \
          terragrunt destroy --terragrunt-non-interactive -auto-approve -input=false )
      destroyed_ok=$?
    fi
  elif [ -n "$key" ]; then
    echo "  WARNING: no worktree marker and no live $LIVE_ROOT/$envdir/physical — cannot destroy via terragrunt; leaving state intact." >&2
    destroyed_ok=1
  fi

  # 4) Purge state objects ONLY if the destroy succeeded (else keep state so it can retry).
  if [ "$destroyed_ok" -eq 0 ] || [ -z "$key" ]; then
    aws s3 rm "s3://$BUCKET/$pfx/" --recursive --profile "$P" >/dev/null 2>&1 && echo "  purged state prefix: $pfx"
  else
    echo "  destroy did NOT fully succeed — state prefix kept for retry: $pfx" >&2
    fail=1
  fi
done <<EOF
$PREFIXES
EOF

# 5) Sweep addon-created containerinsights log groups (out-of-band; TF doesn't own them).
for lg in $(aws logs describe-log-groups --region "$R" --profile "$P" \
      --query "logGroups[?starts_with(logGroupName,'/aws/containerinsights/${CUSTOMER}-')].logGroupName" --output text 2>/dev/null); do
  aws logs delete-log-group --log-group-name "$lg" --region "$R" --profile "$P" 2>/dev/null && echo "  deleted log group: $lg"
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

# 7) Verify.
echo; echo "==================== verify-clean ===================="
DR_REGION="$DR" AWS_PROFILE="$P" "$HARNESS_DIR/verify-clean.sh" "$CUSTOMER" || fail=1

echo
if [ "$fail" -eq 0 ]; then echo "CLEANUP COMPLETE — account is clean."; else
  echo "CLEANUP INCOMPLETE — see warnings above (re-run after resolving)."; exit 1; fi
