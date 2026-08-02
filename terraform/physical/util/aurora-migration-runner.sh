#!/usr/bin/env bash
# RDS -> Aurora Serverless v2 migration runner. Drives the DB-side phases the
# aurora_migration_state Terraform (aurora-migration.tf) cannot: native schema
# pre-load, DMS task orchestration, validation gates, and the fence/cutover
# sequence. Every step is the 2026-07-26 rehearsal procedure, productionized
# (internal design doc: aurora-dms-migration-design; review fixes folded in).
#
# Runs on an operator machine with aws cli + jq; all SQL executes ON THE ENV
# BASTION via SSM (the bastion has mariadb installed by its State Manager
# association). Nothing here talks to a database directly.
#
# Usage:
#   AWS_PROFILE=... aurora-migration-runner.sh <identifier> <region> <phase>
# Phases, in order:
#   status         - show everything (safe anytime)
#   preload        - schema dump/split, dangling-FK scan, object artifact dump,
#                    base DDL, information_schema md5 gate
#   load           - assessment, full load to the automatic cached-changes stop,
#                    deferred indexes then FKs (= orphan check), Initstmt
#                    removal while stopped + endpoint re-test, CDC resume
#   load-tail      - RECOVERY: load's post-full-load steps only, for a runner
#                    that died after the automatic stop (guards on the
#                    STOPPED_AFTER_CACHED_EVENTS stop reason)
#   fk-resume      - RECOVERY: continue an interrupted deferred-FK apply by
#                    diffing live constraint names against deferred-fk.sql,
#                    then the same endpoint/CDC tail as load-tail
#   validate       - DMS validation whitelist gate + table checksums
#                    (CHECKSUM_TABLES="db.t1 db.t2" env var adds tables)
#   harden         - install the STOP_TASK apply-error policies into the task's
#                    EFFECTIVE settings (stop/modify/resume). Required once per
#                    task provisioned before the policies landed in the JSON;
#                    fence refuses to run without them. Safe mid-soak.
#   fence          - marker + Terraform-owned read_only fence + negative test +
#                    go-live gate + object install + final task stop.
#                    Interactive: pauses for the fenced=true Spacelift apply.
#   cutover-verify - POST-promotion verification (the gated physical cutover
#                    apply + its auto-following logical apply ARE the promotion;
#                    the point of no return is the logical apply repointing the
#                    app - gate BEFORE confirming physical, not here).
#   abort          - pre-cutover abort with hard guards; refuses after cutover.
#   bi-epoch       - reload the BI DMS task from the (now Aurora) source.
#
# Environment overrides:
#   FENCE_PREFLIGHT=warn         report the fence pre-flight blockers and carry on
#                                instead of refusing. The gate exists to stop a
#                                fence landing behind a backup (measured 262.89s
#                                vs a normal 3.3-6.7s, and unabortable while it
#                                runs); this is the escape hatch for when the
#                                cutover has to proceed regardless.
#   FENCE_BACKUP_MARGIN_MIN=30   how close to the source's backup window is too
#                                close to start a fence.
#   CHECKSUM_TABLES="db.t1 ..."  extra tables for the validate phase.
#   LEGACY_DB_SECRET=<prefix>    fallback prefix for the primary DB secret.
set -euo pipefail

ID="${1:?identifier (e.g. <customer>-<env>)}"; REGION="${2:?region}"; PHASE="${3:?phase}"
SRC_ID="$ID"; CLUSTER_ID="$ID"; TASK_ID="${ID}-aurora-migration"

say() { printf '%s %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
die() { say "FATAL: $*" >&2; exit 1; }

# --- discovery ---------------------------------------------------------------
bastion_id() {
  # Pin the Name tag to "<id>-bastion", NOT tag:Environment. A migrated
  # account can keep both the current and the pre-migration legacy bastion,
  # and BOTH carry Environment=<env>. Matching on Environment alone grabs
  # whichever Reservations[0] happens to be first - often the legacy bastion,
  # which sits in a different VPC and cannot route to the RDS (connect times
  # out).
  local id
  id=$(aws ec2 describe-instances --region "$REGION" \
    --filters "Name=tag:Role,Values=Bastion" "Name=instance-state-name,Values=running" \
               "Name=tag:Name,Values=${ID}-bastion" \
    --query 'Reservations[].Instances[].InstanceId' --output text | tr '\t' '\n' | grep . | head -1)
  [ -n "$id" ] || die "no running bastion named ${ID}-bastion in $REGION"
  printf '%s' "$id"
}
rds_endpoint() { aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" --query 'DBInstances[0].Endpoint.Address' --output text; }
aurora_endpoint() { aws rds describe-db-clusters --db-cluster-identifier "$CLUSTER_ID" --region "$REGION" --query 'DBClusters[0].Endpoint' --output text; }
task_arn() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].ReplicationTaskArn' --output text; }
task_status() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].Status' --output text 2>/dev/null || echo none; }
# The BI replication is DMS Serverless (a replication-config); the migration task above is
# still provisioned. Two different API surfaces, so probe once and branch. Getting this wrong
# is silent rather than loud: describe-replication-tasks simply does not return a
# replication-config, so bi_task_status would report "none", the fence would skip stopping BI
# without saying so, and bi-epoch would try to reload a task that does not exist.
#
# Both probes below FAIL CLOSED. An earlier version swallowed the describe with
# `2>/dev/null || true` and let the fall-through arm mean "task", so a denied or throttled call
# - the empty string - classified as a provisioned task. That is the worst possible default
# during the fence: bi_task_status would then query describe-replication-tasks, get "None" on
# a serverless stack, and the fence would skip stopping a LIVE replication before repointing
# its source endpoint. The trigger is not hypothetical: a runner role that predates the
# serverless permissions (dms:DescribeReplicationConfigs, dms:DescribeReplications) misreads
# every stack, every run, silently. BI_KIND is cached, so one bad call would poison the run.
#
# So: serverless requires a replication-config ARN, task requires a replication-task ARN, and
# anything else - error, unreachable API, or both absent - either dies or is reported as
# "none". Classification is never inferred from an absence.
BI_KIND=""
bi_kind() {
  if [ -z "$BI_KIND" ]; then
    local out rc
    out=$(aws dms describe-replication-configs --region "$REGION" \
      --filters "Name=replication-config-id,Values=$ID" \
      --query 'ReplicationConfigs[0].ReplicationConfigArn' --output text 2>&1) && rc=0 || rc=$?
    case "$out" in
      *:replication-config:*) BI_KIND=serverless ;;
      *)
        # Only two shapes are allowed to mean "not serverless": a successful call that returned
        # nothing (empty list, --query prints "None", exit 0) and an explicit not-found fault
        # (exit 254 - what this filter raises today). Every other failure is an unknown, and an
        # unknown must not become a classification.
        if [ "$rc" -ne 0 ] && ! printf '%s' "$out" | grep -q 'ResourceNotFoundFault'; then
          die "cannot determine the BI replication kind: describe-replication-configs failed (exit $rc): $(printf '%s' "$out" | tr '\n' ' ')"
        fi
        # Positively confirm a provisioned task rather than assuming one.
        local tout trc
        tout=$(aws dms describe-replication-tasks --region "$REGION" \
          --filters "Name=replication-task-id,Values=$ID" \
          --query 'ReplicationTasks[0].ReplicationTaskArn' --output text 2>&1) && trc=0 || trc=$?
        case "$tout" in
          *:rep:*|*:replication-task:*) BI_KIND=task ;;
          *)
            if [ "$trc" -ne 0 ] && ! printf '%s' "$tout" | grep -q 'ResourceNotFoundFault'; then
              die "cannot determine the BI replication kind: describe-replication-tasks failed (exit $trc): $(printf '%s' "$tout" | tr '\n' ' ')"
            fi
            # Neither exists. Legitimate on a stack with BI switched off; callers branch on it.
            BI_KIND=none
            ;;
        esac
        ;;
    esac
  fi
  printf '%s' "$BI_KIND"
}
bi_task_arn() {
  case "$(bi_kind)" in
    serverless) aws dms describe-replication-configs --region "$REGION" --filters "Name=replication-config-id,Values=$ID" --query 'ReplicationConfigs[0].ReplicationConfigArn' --output text ;;
    task) aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$ID" --query 'ReplicationTasks[0].ReplicationTaskArn' --output text ;;
    *) die "no BI replication (config or task) exists for $ID" ;;
  esac
}
BI_ARN=""
bi_arn_cached() { [ -n "$BI_ARN" ] || BI_ARN=$(bi_task_arn); printf '%s' "$BI_ARN"; }
# Emits one of: a real DMS status, "not-started" (serverless config that has never run, so
# describe-replications returns an empty list), or "none" (no BI at all). Dies on API error -
# see the fail-closed note above; a status this cannot read is not a status of "not running".
bi_task_status() {
  local out rc
  case "$(bi_kind)" in
    serverless)
      # Cached ARN: this is called every 10s in the stop/reload wait loops, and resolving the
      # config ARN inside the filter each time doubled the API calls per iteration for no gain.
      out=$(aws dms describe-replications --region "$REGION" --filters "Name=replication-config-arn,Values=$(bi_arn_cached)" --query 'Replications[0].Status' --output text 2>&1) && rc=0 || rc=$?
      ;;
    task)
      out=$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$ID" --query 'ReplicationTasks[0].Status' --output text 2>&1) && rc=0 || rc=$?
      ;;
    *) printf 'none'; return 0 ;;
  esac
  if [ "$rc" -ne 0 ]; then
    printf '%s' "$out" | grep -q 'ResourceNotFoundFault' && { printf 'not-started'; return 0; }
    die "cannot read the BI replication status (exit $rc): $(printf '%s' "$out" | tr '\n' ' ')"
  fi
  [ "$out" = "None" ] && { printf 'not-started'; return 0; }
  printf '%s' "$out"
}
bi_stop() {
  if [ "$(bi_kind)" = serverless ]; then
    aws dms stop-replication --replication-config-arn "$(bi_task_arn)" --region "$REGION" >/dev/null
  else
    aws dms stop-replication-task --replication-task-arn "$(bi_task_arn)" --region "$REGION" >/dev/null
  fi
}
# $1 = resume-processing | reload-target. Serverless keeps both, and reload-target means the
# same thing on either. Note a serverless replication left stopped for 48h is deprovisioned
# and cannot be resumed at all, so do not park BI stopped between fence and bi-epoch.
bi_start() {
  if [ "$(bi_kind)" = serverless ]; then
    aws dms start-replication --replication-config-arn "$(bi_task_arn)" --start-replication-type "$1" --region "$REGION" >/dev/null
  else
    aws dms start-replication-task --replication-task-arn "$(bi_task_arn)" --start-replication-task-type "$1" --region "$REGION" >/dev/null
  fi
}
# Bounded BI stop wait, same shape as wait_task_status. The callers used to spin on
# `while [ "$(bi_task_status)" != "stopped" ]` with no bound and no failure states, so a BI
# replication that failed on the way down hung the fence forever with the source already
# read-only and customers waiting - and, on serverless, against the 48h deprovision clock.
bi_wait_stopped() { # $1=timeout seconds
  local waited=0 s
  while :; do
    s=$(bi_task_status)
    [ "$s" = stopped ] && return 0
    case "$s" in
      failed) die "the BI replication entered 'failed' while stopping - it will not reach 'stopped'. Investigate before continuing; a serverless replication left failed for 48h is deprovisioned and cannot be resumed." ;;
      deprovisioning | deprovisioned) die "the BI replication is $s (stopped or failed for 48h) - it cannot be resumed; recreate it with a physical apply" ;;
      none) die "the BI replication disappeared while stopping" ;;
    esac
    [ "$waited" -ge "$1" ] && die "the BI replication did not stop in ${1}s (status=$s)"
    sleep 10
    waited=$((waited + 10))
  done
}
# NOTE: the projection query has NO `| [0]`. A `| [0]` inside --query is a
# pipe-expression the AWS CLI evaluates PER PAGE of paginated list-secrets
# results, so a page without a match emits "None" and the match on a later page
# emits the ARN - the caller then sees "None" first and the whole chain fails.
# The bare projection aggregates across every page; we pick the first real ARN.
first_secret_arn() { # $1 = name prefix
  aws secretsmanager list-secrets --region "$REGION" --query "SecretList[?starts_with(Name, '$1')].ARN" --output text | tr '\t' '\n' | grep -v '^None$' | grep . | head -1
}
# Some migrated envs carry a legacy-named credentials secret instead of
# <id>-database. Set LEGACY_DB_SECRET to that name and the DB-side phases fall
# back to it when the standard name resolves nothing.
primary_secret() {
  local arn
  arn=$(first_secret_arn "${ID}-database")
  [ -n "$arn" ] || { [ -n "${LEGACY_DB_SECRET:-}" ] && arn=$(first_secret_arn "$LEGACY_DB_SECRET"); }
  printf '%s' "$arn"
}
migration_secret() { first_secret_arn "${ID}-aurora-migration"; }
src_pg() { aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" --query 'DBInstances[0].DBParameterGroups[0].DBParameterGroupName' --output text; }
primary_secret_host() { aws secretsmanager get-secret-value --region "$REGION" --secret-id "$(primary_secret)" --query SecretString --output text | jq -r .host; }

# --- fence pre-flight: RDS defers parameter applies behind backups ------------
# MEASURED 2026-08-02 (ddvtest, RDS 8.0.46, both db.t3.medium and db.m5.8xlarge):
# the read_only fence lands in 3.3-6.7s on a quiet instance. One run took 262.89s
# because creating a read replica had triggered a backup (RDS events: "Backing up
# DB instance 06:16:10" -> "Finished 06:19:33") and the parameter apply landed 9s
# AFTER the backup finished. RDS queues the apply behind the backup.
#
# The duration is not the worst of it. For that whole window the UNFENCE is also
# refused - ModifyDBParameterGroup returns InvalidDBParameterGroupState ("this
# parameter group cannot be modified because it is currently being applied"),
# reproduced deterministically. So a backup-contended fence is an UNABORTABLE
# fence: the source is read-only, customers are down, and the documented rollback
# is unavailable until the backup completes.
#
# This gate converts an 80x timing variance from a surprise during the fence into
# a decision before it. It does NOT suppress automated backups: dropping
# BackupRetentionPeriod to 0 deletes the existing automated backups, which is the
# rollback path for the very cutover being run. Schedule around the window, do
# not disarm it.
#
# Override with FENCE_PREFLIGHT=warn to report and continue. A stuck or
# misreported backup must never be able to strand a cutover that has to proceed.
hhmm_to_min() { printf '%s' "$1" | awk -F: '{ print ($1 * 60) + $2 }'; }

# Minutes until the source's backup window NEXT opens, or 9999 if no window is
# set. Inside the window it returns the wait until tomorrow's, which is why the
# caller must ask backup_window_open first and only fall through to this.
minutes_to_backup_window() {
  local w start now delta
  w=$(aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" \
      --query 'DBInstances[0].PreferredBackupWindow' --output text 2>/dev/null || echo None)
  { [ -n "$w" ] && [ "$w" != None ]; } || { printf '9999'; return; }
  start=$(hhmm_to_min "${w%%-*}"); now=$(hhmm_to_min "$(date -u +%H:%M)")
  delta=$(( start - now ))
  # spelled as if/fi rather than "[ test ] && assign" so the exit status of the
  # last statement before the printf is never the false branch of a test
  if [ "$delta" -lt 0 ]; then delta=$(( delta + 1440 )); fi
  printf '%s' "$delta"
}

backup_window_open() {
  local w start end now
  w=$(aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" \
      --query 'DBInstances[0].PreferredBackupWindow' --output text 2>/dev/null || echo None)
  { [ -n "$w" ] && [ "$w" != None ]; } || return 1
  start=$(hhmm_to_min "${w%%-*}"); end=$(hhmm_to_min "${w##*-}"); now=$(hhmm_to_min "$(date -u +%H:%M)")
  # a window may wrap midnight (e.g. 23:30-00:00), so the two cases differ
  if [ "$start" -le "$end" ]; then [ "$now" -ge "$start" ] && [ "$now" -lt "$end" ]
  else [ "$now" -ge "$start" ] || [ "$now" -lt "$end" ]; fi
}

# Every reason the next parameter apply could be deferred or refused, as
# "code: detail" lines. Empty output means clear to fence.
fence_blockers() {
  local st pg_status pending snaps mins
  st=$(aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" \
       --query 'DBInstances[0].DBInstanceStatus' --output text 2>/dev/null || echo unknown)
  # "backing-up" is the state that produced the 262.89s apply. "modifying",
  # "upgrading" and friends queue applies the same way; anything that is not
  # plainly available is a reason to stop and look.
  [ "$st" = available ] || echo "instance-status: $st (only 'available' applies parameters promptly)"

  # An apply already in flight is what makes the fence unabortable. Refuse to add
  # a second one on top of it.
  pg_status=$(aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" \
              --query 'DBInstances[0].DBParameterGroups[0].ParameterApplyStatus' --output text 2>/dev/null || echo unknown)
  [ "$pg_status" = in-sync ] || echo "parameter-group: $pg_status (an apply is in flight; the unfence would be refused)"

  pending=$(aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" \
            --query 'DBInstances[0].PendingModifiedValues' --output json 2>/dev/null | jq -rc 'if (. // {}) == {} then "" else tostring end')
  [ -z "$pending" ] || echo "pending-modifications: $pending"

  # Manual snapshots defer applies exactly as automated backups do, and a cutover
  # runbook that takes a safety snapshot first is the likely way to hit this.
  snaps=$(aws rds describe-db-snapshots --db-instance-identifier "$SRC_ID" --region "$REGION" \
          --query 'DBSnapshots[?Status!=`available`].[DBSnapshotIdentifier,Status]' --output text 2>/dev/null | tr '\t' '=' | tr '\n' ' ' | sed 's/ *$//')
  [ -z "$snaps" ] || echo "snapshot-in-progress: $snaps"

  if backup_window_open; then
    echo "backup-window: OPEN now (a backup may start or be running)"
  else
    mins=$(minutes_to_backup_window)
    # A non-integer here means the probe itself broke. Report that rather than
    # letting [ -ge ] error out, which reads like a backup-window finding and
    # sends the operator looking in the wrong place.
    case "$mins" in
      '' | *[!0-9]*) echo "backup-window: UNKNOWN (probe returned '$mins' - treat as unsafe and check by hand)" ;;
      *) [ "$mins" -ge "${FENCE_BACKUP_MARGIN_MIN:-30}" ] || \
           echo "backup-window: opens in ${mins}m (under the ${FENCE_BACKUP_MARGIN_MIN:-30}m margin; the fence could be caught by it)" ;;
    esac
  fi
}

# $1 = a short label for the log line, so the two call sites are distinguishable
preflight_fence_gate() {
  local blockers mode="${FENCE_PREFLIGHT:-enforce}"
  say "pre-flight ($1): checking nothing will defer the parameter apply"
  blockers=$(fence_blockers)
  if [ -z "$blockers" ]; then
    say "  clear - instance available, no apply in flight, no snapshot running, backup window not near"
    return 0
  fi
  say "  BLOCKED:"; printf '%s\n' "$blockers" | sed 's/^/    - /'
  if [ "$mode" = warn ]; then
    say "  FENCE_PREFLIGHT=warn - continuing anyway. Expect a slow fence and an unabortable window."
    return 0
  fi
  die "fence pre-flight failed (see above). A parameter apply issued now can be deferred behind a
backup or snapshot - measured at 262.89s against a normal 3.3-6.7s - and the unfence is REFUSED for
that whole window, so the abort path is unavailable while it lasts. Wait for the condition to clear
and re-run. To proceed regardless: FENCE_PREFLIGHT=warn $0 $ID $REGION $PHASE"
}

# DMS validation clean-state whitelist: anything else is unclean. Matched
# case-insensitively ("No primary key" casing differs across DMS versions).
unclean_validation_count() {
  aws dms describe-table-statistics --replication-task-arn "$(task_arn)" --region "$REGION" \
    --query 'TableStatistics[].ValidationState' --output text | tr '\t' '\n' | \
    awk '{ l=tolower($0) } l != "validated" && l != "no primary key" && NF { c++ } END { print c+0 }'
}

# The four CDC apply-error policies, from the task's EFFECTIVE settings (what
# the task actually runs with - the Terraform JSON is create-time only, its
# replication_task_settings is under ignore_changes, so an already-provisioned
# task keeps whatever it was born with).
effective_apply_policies() {
  aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" \
    --query 'ReplicationTasks[0].ReplicationTaskSettings' --output text | \
    jq -r '.ErrorBehavior | [.ApplyErrorInsertPolicy, .ApplyErrorUpdatePolicy, .ApplyErrorDeletePolicy, .ApplyErrorEscalationPolicy] | join(",")'
}

# Bounded task-state wait. Dies on the DMS failure states and on timeout - an
# unbounded poll here would hang the fence (source read-only, customers waiting)
# on a task that will never reach the wanted state.
wait_task_status() { # $1=wanted status $2=timeout seconds
  local waited=0 s
  while :; do
    s=$(task_status)
    [ "$s" = "$1" ] && return 0
    case "$s" in failed|stopped-after-fail|deleting) die "task entered '$s' while waiting for '$1'";; esac
    # "none" (describe hiccup) is NOT fatal here - the timeout bounds it.
    [ "$waited" -ge "$2" ] && die "task did not reach '$1' in ${2}s (status=$s)"
    sleep 10; waited=$((waited+10))
  done
}

# Parse "mysql-bin-changelog.NNNN / POS" style coordinates out of the task's
# RecoveryCheckpoint ("checkpoint:V1#...#$.NNNN:POS:...") and compare with the
# fenced source's final coordinate. Call this ONLY after the task has stopped:
# RecoveryCheckpoint is a stopped-task safepoint that lags the live apply while
# the task runs and flushes to the true applied commit position only on stop.
# Stop + this comparison together are the drain proof.
checkpoint_reached() { # $1=final_file_seq $2=final_pos
  local ck m seq pos
  ck=$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].RecoveryCheckpoint' --output text)
  # Two checkpoint spellings observed in the wild: "mysql-bin-changelog.009875:153"
  # (documented) and the abbreviated "$.583860:1357" (seen live in the rehearsal).
  m=$(echo "$ck" | grep -oE 'mysql-bin-changelog\.[0-9]+:[0-9]+' | head -1)
  [ -n "$m" ] && { seq=$(echo "$m" | cut -d. -f2 | cut -d: -f1); pos=$(echo "$m" | cut -d: -f2); }
  if [ -z "${seq:-}" ]; then
    m=$(echo "$ck" | grep -oE '\$\.[0-9]+:[0-9]+' | head -1)
    [ -n "$m" ] && { seq=$(echo "$m" | cut -d. -f2 | cut -d: -f1); pos=$(echo "$m" | cut -d: -f2); }
  fi
  [ -n "${seq:-}" ] && [ -n "${pos:-}" ] || return 1
  seq=$((10#$seq)); pos=$((10#$pos))
  [ "$seq" -gt "$1" ] || { [ "$seq" -eq "$1" ] && [ "$pos" -ge "$2" ]; }
}

# --- bastion exec (SSM) ------------------------------------------------------
# A dead local runner does not cancel an already-dispatched remote command, so
# a recovery phase started too eagerly can overlap the DDL it is recovering.
# Refuse while anything is still running on the bastion; an operator who has
# confirmed the remote side is a zombie can override with SKIP_SSM_BUSY_CHECK=1.
ssm_refuse_if_busy() {
  [ -n "${SKIP_SSM_BUSY_CHECK:-}" ] && return 0
  local busy
  # jq over merged json, not --query with text output: text emits one count
  # PER RESULT PAGE, so a long invocation history yields "0\n0" and a false
  # refusal. All nonterminal states count as busy.
  busy=$(aws ssm list-command-invocations --region "$REGION" --instance-id "$(bastion_id)" --output json \
         | jq '[.CommandInvocations[] | select(.Status=="InProgress" or .Status=="Pending" or .Status=="Delayed" or .Status=="Cancelling")] | length')
  [ "$busy" = "0" ] || die "$busy SSM command(s) still running on the bastion - the prior dispatch may be alive; refusing to overlap (SKIP_SSM_BUSY_CHECK=1 overrides)"
}

# Optional $1 raises the per-command executionTimeout from the 3600s default.
# Only the deferred-DDL applies pass a higher cap (a serial FK apply on a large
# env runs for hours and died at the default partway through, which is what
# fk-resume recovers); everything else keeps the tight bound so a hung command
# during a read-only window fails fast instead of squatting for six hours.
ssm_run() {
  local exec_timeout="${1:-3600}" script cid status
  script="$(cat)"
  cid=$(aws ssm send-command --region "$REGION" --instance-ids "$(bastion_id)" \
        --document-name AWS-RunShellScript --timeout-seconds 5400 \
        --parameters "$(jq -n --arg c "$script" --arg t "$exec_timeout" '{commands: [$c], executionTimeout: [$t]}')" \
        --query 'Command.CommandId' --output text)
  while :; do
    status=$(aws ssm get-command-invocation --region "$REGION" --command-id "$cid" --instance-id "$(bastion_id)" --query Status --output text 2>/dev/null || echo Pending)
    case "$status" in Success|Failed|Cancelled|TimedOut) break;; esac; sleep 10
  done
  aws ssm get-command-invocation --region "$REGION" --command-id "$cid" --instance-id "$(bastion_id)" --query StandardOutputContent --output text
  [ "$status" = "Success" ] || { aws ssm get-command-invocation --region "$REGION" --command-id "$cid" --instance-id "$(bastion_id)" --query StandardErrorContent --output text >&2; die "bastion step failed ($status)"; }
}

# Source cnf: primary secret credentials + the RDS address PINNED explicitly
# (the secret's host flips to Aurora at cutover - the source cnf must not).
# Target cnf: the migration credentials secret (Aurora's own password, which
# never lives in the primary secret before cutover).
prep_cnfs() {
  local psec msec src tgt
  psec=$(primary_secret); msec=$(migration_secret); src=$(rds_endpoint); tgt=$(aurora_endpoint)
  [ -n "$msec" ] && [ "$msec" != "None" ] || die "migration credentials secret not found - is aurora_migration_state=provision applied?"
  ssm_run <<EOF
set -e; umask 177
P=\$(aws secretsmanager get-secret-value --region $REGION --secret-id "$psec" --query SecretString --output text)
printf '[client]\nhost=%s\nuser=%s\npassword=%s\nssl\n' "$src" "\$(echo "\$P" | jq -r .username)" "\$(echo "\$P" | jq -r .password)" > /tmp/mig-src.cnf
M=\$(aws secretsmanager get-secret-value --region $REGION --secret-id "$msec" --query SecretString --output text)
printf '[client]\nhost=%s\nuser=%s\npassword=%s\nssl\n' "$tgt" "\$(echo "\$M" | jq -r .username)" "\$(echo "\$M" | jq -r .password)" > /tmp/mig-tgt.cnf
echo CNFS_READY
EOF
}

# =============================================================================
# Resolve the BI probes ONCE, in the parent shell, before dispatching. Inside
# a $( ) substitution a die() only exits the subshell: the caller reads an
# empty string, bi_task_status falls through to "none", and the fence skips
# stopping a LIVE replication - the exact fail-open these probes exist to
# prevent. Resolved here, a probe failure (say, a runner role without the
# serverless describe permissions) kills the run before any phase acts, and
# every later $(bi_kind) / $(bi_arn_cached) is a pure cache read, which is
# what the caching was for. The cost is that phases which never touch BI also
# need the DMS describe permissions; that is the fail-closed trade.
bi_kind >/dev/null
[ "$(bi_kind)" = none ] || bi_arn_cached >/dev/null

case "$PHASE" in

status)
  say "migration task: $(task_status)   bi task: $(bi_task_status)"
  say "rds: $(rds_endpoint 2>/dev/null || echo none)"
  say "aurora: $(aurora_endpoint 2>/dev/null || echo none)"
  say "primary secret host: $(primary_secret_host 2>/dev/null || echo unknown)  (aurora after cutover)"
  # Read-only preview of the fence gate, so the window can be chosen before the
  # cutover call rather than discovered at the fence.
  PF=$(fence_blockers 2>/dev/null || echo "preflight-probe-failed")
  if [ -z "$PF" ]; then say "fence pre-flight: clear"
  else say "fence pre-flight: BLOCKED"; printf '%s\n' "$PF" | sed 's/^/    - /'; fi
  ;;

preload)
  prep_cnfs
  ssm_run <<'EOF'
set -e; cd /tmp; rm -rf mig && mkdir mig && cd mig
S="mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names"
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names --init-command='SET SESSION restrict_fk_on_non_standard_key=0'"
$S -e "SELECT @@binlog_format, @@binlog_row_image, @@log_bin" | grep -q "ROW	FULL	1" || { echo "SOURCE NOT CDC-READY"; exit 1; }
[ "$($S -e 'SELECT @@lower_case_table_names')" = "$(eval $T -e "'SELECT @@lower_case_table_names'")" ] || { echo LCTN_MISMATCH; exit 1; }
# local_infile is no longer pinned in the cluster PG (default-valued pins are a silent
# no-op that drift forever); the full load depends on it, so assert the effective value.
# Empty means the query never ran, which is a different problem than the setting being off
LI="$(eval $T -e "'SELECT @@local_infile'")"
[ -n "$LI" ] || { echo TARGET_LOCAL_INFILE_UNREADABLE; exit 1; }
[ "$LI" = "1" ] || { echo "TARGET_LOCAL_INFILE_OFF ($LI)"; exit 1; }
$S -e "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('mysql','information_schema','performance_schema','sys','innodb','tmp') AND schema_name NOT LIKE 'awsdms%'" > schemas.txt
# dangling FKs: unenforceable residue, excluded from the target and reported
$S -e "SELECT CONCAT(k.constraint_schema,'.',k.table_name,'.',k.constraint_name) FROM information_schema.key_column_usage k LEFT JOIN information_schema.tables t ON t.table_schema=k.referenced_table_schema AND t.table_name=k.referenced_table_name WHERE k.referenced_table_name IS NOT NULL AND t.table_name IS NULL" > dangling-fks.txt
echo "dangling FKs: $(wc -l < dangling-fks.txt)"; cat dangling-fks.txt
# object artifacts: routines/events/triggers are NOT migrated by DMS and NOT in
# base DDL. Dumped here; installed on the target by fence (after the final task
# stop, so triggers can never fire on DMS-applied DML). Empty on the fleet
# this was written for.
mariadb-dump --defaults-extra-file=/tmp/mig-src.cnf --no-data --no-create-info --no-create-db --routines --events --triggers --skip-lock-tables --no-tablespaces --databases $(tr '\n' ' ' < schemas.txt) > objects.sql
echo "object artifact counts: routines=$($S -e "SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema NOT IN ('mysql','sys')") triggers=$($S -e "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema NOT IN ('mysql','sys')") events=$($S -e "SELECT COUNT(*) FROM information_schema.events WHERE event_schema NOT IN ('mysql','sys')")"
mariadb-dump --defaults-extra-file=/tmp/mig-src.cnf --no-data --skip-triggers --skip-lock-tables --no-tablespaces --databases $(tr '\n' ' ' < schemas.txt) > full.sql
python3 - <<'PYEOF'
import re
base, deferred, buf, cur_t, cur_s, inc = [], [], [], None, None, False
dangling = set()
for l in open('dangling-fks.txt'):
    p = l.strip().split('.')
    if len(p) == 3: dangling.add((p[0], p[2]))
def flush():
    for i in range(len(buf)-1, -1, -1):
        s = buf[i].strip()
        if s.startswith(')'):
            j = i-1
            while j >= 0 and not buf[j].strip(): j -= 1
            if j >= 0 and buf[j].rstrip().endswith(','): buf[j] = buf[j].rstrip()[:-1]+'\n'
            break
    base.extend(buf); buf.clear()
for line in open('full.sql', errors='replace'):
    if line.startswith('CREATE TABLE'):
        inc = True; m = re.match(r'CREATE TABLE `([^`]+)`', line); cur_t = m.group(1) if m else None
        buf.append(line); continue
    if line.startswith('USE `') or 'CREATE DATABASE' in line:
        m = re.search(r'`([^`]+)`', line); cur_s = m.group(1) if m else cur_s
        base.append(line); continue
    if inc:
        s = line.strip(); fq = f"`{cur_s}`.`{cur_t}`"
        cm = re.match(r'CONSTRAINT `([^`]+)` FOREIGN KEY', s)
        if cm and 'FOREIGN KEY' in s:
            if (cur_s, cm.group(1)) not in dangling:
                deferred.append(f"ALTER TABLE {fq} ADD {s.rstrip(',')};\n")
            continue
        if re.match(r'^(KEY|INDEX|FULLTEXT KEY|SPATIAL KEY) ', s):
            deferred.append(f"ALTER TABLE {fq} ADD {s.rstrip(',')};\n"); continue
        buf.append(line)
        if s.startswith(')') and s.endswith(';'): inc = False; flush()
    else:
        base.append(line)
open('base.sql','w').writelines(base)
open('deferred-idx.sql','w').writelines([l for l in deferred if 'FOREIGN KEY' not in l])
open('deferred-fk.sql','w').writelines([l for l in deferred if 'FOREIGN KEY' in l])
print(f"base={len(base)} idx={sum('FOREIGN KEY' not in l for l in deferred)} fk={sum('FOREIGN KEY' in l for l in deferred)}")
PYEOF
eval $T < base.sql; echo BASE_APPLIED
Q="SELECT c.table_schema,c.table_name,c.column_name,c.column_type,c.is_nullable,IFNULL(c.column_default,'~N~'),c.extra FROM information_schema.columns c JOIN information_schema.tables t ON c.table_schema=t.table_schema AND c.table_name=t.table_name WHERE t.table_type='BASE TABLE' AND c.table_schema NOT IN ('mysql','information_schema','performance_schema','sys','innodb','tmp') AND c.table_schema NOT LIKE 'awsdms%' ORDER BY c.table_schema,c.table_name,c.ordinal_position"
$S -e "$Q" > cols-src.tsv; eval $T -e "\"$Q\"" > cols-tgt.tsv
[ "$(md5sum < cols-src.tsv)" = "$(md5sum < cols-tgt.tsv)" ] && echo MD5_GATE_PASS || { echo MD5_GATE_FAIL; diff cols-src.tsv cols-tgt.tsv | head -20; exit 1; }
EOF
  say "preload complete - md5 gate passed; review dangling FKs + object counts above"
  ;;

load)
  [ "$(task_status)" = "ready" ] || die "task not ready (status=$(task_status))"
  if aws dms start-replication-task-assessment --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null 2>&1; then
    # The legacy assessment flips the task to "testing" and back to "ready" when
    # its report is written. StartReplicationTask is refused (InvalidResourceState)
    # while "testing", so wait for the task to settle before starting the load.
    say "premigration assessment started - waiting for it to settle"
    AWAIT=0
    while :; do s=$(task_status); [ "$s" = "ready" ] && break
      [ "$s" = "failed" ] && die "task failed during assessment"
      AWAIT=$((AWAIT+1)); [ "$AWAIT" -gt 60 ] && die "assessment did not settle to ready in 10m (status=$s)"
      say "  assessment: task=$s"; sleep 10
    done
  else
    say "WARN: assessment API unavailable - preload inventory gates stand in"
  fi
  say "starting full load + CDC"
  aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type start-replication --region "$REGION" >/dev/null
  say "waiting for the automatic post-full-load stop"
  while :; do s=$(task_status); say "  task=$s"; [ "$s" = "stopped" ] && break; [ "$s" = "failed" ] && die "task failed - check the DMS task log"; sleep 60; done
  say "applying deferred DDL (indexes, then FKs = orphan check) while quiescent"
  prep_cnfs
  # Stage markers make an interruption diagnosable: the DMS stop reason alone
  # cannot distinguish "indexes not yet applied" from "died mid-FK", and the
  # recovery phases below key off these files to prove which point was reached.
  ssm_run 21600 <<'EOF'
set -e; cd /tmp/mig
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --init-command='SET SESSION restrict_fk_on_non_standard_key=0'"
eval $T < deferred-idx.sql; touch .idx-applied; echo IDX_APPLIED
eval $T < deferred-fk.sql;  touch .fk-applied;  echo FK_APPLIED_ZERO_ORPHANS
EOF
  say "removing the FK-off Initstmt from the target endpoint (task stopped)"
  TEP=$(aws dms describe-endpoints --region "$REGION" --filters "Name=endpoint-id,Values=${ID}-aurora-migration-target" --query 'Endpoints[0].EndpointArn' --output text)
  aws dms modify-endpoint --endpoint-arn "$TEP" --extra-connection-attributes "" --region "$REGION" >/dev/null
  RIARN=$(aws dms describe-replication-instances --region "$REGION" --filters "Name=replication-instance-id,Values=${ID}-aurora-migration" --query 'ReplicationInstances[0].ReplicationInstanceArn' --output text)
  # modify-endpoint auto-triggers its own connection test, so issuing ours races
  # with "Connection is already being tested" (InvalidResourceState). Tolerate
  # that - the poll below is the real gate - and bound the wait so a genuinely
  # broken endpoint fails loudly instead of looping forever.
  aws dms test-connection --replication-instance-arn "$RIARN" --endpoint-arn "$TEP" --region "$REGION" >/dev/null 2>&1 || true
  CWAIT=0
  while :; do
    cs=$(aws dms describe-connections --region "$REGION" --filters "Name=endpoint-arn,Values=$TEP" --query 'Connections[0].Status' --output text 2>/dev/null || echo none)
    [ "$cs" = "successful" ] && break
    [ "$cs" = "failed" ] && die "target endpoint connection test failed after Initstmt removal"
    CWAIT=$((CWAIT+1)); [ "$CWAIT" -gt 30 ] && die "target endpoint connection did not go successful in 5m (status=$cs)"
    sleep 10
  done
  say "resuming CDC (fresh sessions, FK checks ON)"
  aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
  while [ "$(task_status)" != "running" ]; do sleep 10; done
  say "load complete - CDC running. Soak >= 1 day, then 'validate'."
  ;;

load-tail)
  # Recovery for an interrupted load phase: the full load finished and the task
  # took its automatic STOPPED_AFTER_CACHED_EVENTS stop, but the runner died
  # before the deferred DDL / Initstmt removal / CDC resume. Same commands as
  # the load phase from that point on; guards refuse any other task state.
  # Written for a production run whose driving session died mid-load; the
  # source binlog retention (48h via rds_set_configuration) is what makes the
  # late CDC resume safe - check it before leaning on this after a long gap.
  [ "$(task_status)" = "stopped" ] || die "task not stopped (status=$(task_status)) - load-tail only recovers the post-full-load stop"
  SR=$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].StopReason' --output text)
  echo "$SR" | grep -q "STOPPED_AFTER_CACHED_EVENTS" || die "stop reason is '$SR', not STOPPED_AFTER_CACHED_EVENTS - refusing"
  ssm_refuse_if_busy
  prep_cnfs
  say "applying deferred indexes while quiescent"
  ssm_run 21600 <<'EOF'
set -e; cd /tmp/mig
[ -f .idx-applied ] && { echo IDX_ALREADY_APPLIED; exit 0; }
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --init-command='SET SESSION restrict_fk_on_non_standard_key=0'"
eval $T < deferred-idx.sql; touch .idx-applied; echo IDX_APPLIED
EOF
  say "applying deferred FKs (= orphan check) while quiescent"
  ssm_run 21600 <<'EOF'
set -e; cd /tmp/mig
[ -f .fk-applied ] && { echo FK_ALREADY_APPLIED; exit 0; }
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --init-command='SET SESSION restrict_fk_on_non_standard_key=0'"
eval $T < deferred-fk.sql;  touch .fk-applied;  echo FK_APPLIED_ZERO_ORPHANS
EOF
  say "removing the FK-off Initstmt from the target endpoint (task stopped)"
  TEP=$(aws dms describe-endpoints --region "$REGION" --filters "Name=endpoint-id,Values=${ID}-aurora-migration-target" --query 'Endpoints[0].EndpointArn' --output text)
  aws dms modify-endpoint --endpoint-arn "$TEP" --extra-connection-attributes "" --region "$REGION" >/dev/null
  RIARN=$(aws dms describe-replication-instances --region "$REGION" --filters "Name=replication-instance-id,Values=${ID}-aurora-migration" --query 'ReplicationInstances[0].ReplicationInstanceArn' --output text)
  aws dms test-connection --replication-instance-arn "$RIARN" --endpoint-arn "$TEP" --region "$REGION" >/dev/null 2>&1 || true
  CWAIT=0
  while :; do
    cs=$(aws dms describe-connections --region "$REGION" --filters "Name=endpoint-arn,Values=$TEP" --query 'Connections[0].Status' --output text 2>/dev/null || echo none)
    [ "$cs" = "successful" ] && break
    [ "$cs" = "failed" ] && die "target endpoint connection test failed after Initstmt removal"
    CWAIT=$((CWAIT+1)); [ "$CWAIT" -gt 30 ] && die "target endpoint connection did not go successful in 5m (status=$cs)"
    sleep 10
  done
  say "resuming CDC (fresh sessions, FK checks ON)"
  aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
  while [ "$(task_status)" != "running" ]; do sleep 10; done
  say "load complete - CDC running. Soak, then 'validate'."
  ;;

fk-resume)
  # Recovery for an interrupted deferred-FK apply (SSM executionTimeout killed
  # the serial mariadb session partway). DDL is per-statement atomic and the
  # apply is strictly serial, so the target holds an exact prefix of
  # deferred-fk.sql; filter the already-present constraint names out and apply
  # the rest, then run the same endpoint/CDC tail as load-tail. The name diff
  # is only valid under that exact-prefix assumption - it cannot detect a
  # same-named constraint with a different definition, which is why the
  # .idx-applied gate below also matters: it proves the interruption happened
  # inside the FK apply and not somewhere earlier. Statements the parser
  # cannot classify are KEPT, never dropped - re-applying an existing
  # constraint fails loudly, silently skipping a missing one would ship a
  # target without it.
  [ "$(task_status)" = "stopped" ] || die "task not stopped (status=$(task_status))"
  SR=$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].StopReason' --output text)
  echo "$SR" | grep -q "STOPPED_AFTER_CACHED_EVENTS" || die "stop reason is '$SR', not STOPPED_AFTER_CACHED_EVENTS - refusing"
  ssm_refuse_if_busy
  # The stop reason above cannot prove the index phase ran (it is identical
  # before, during and after the deferred DDL). The .idx-applied marker can.
  # An artifact set that predates the markers: verify indexes by hand and set
  # FK_RESUME_ASSUME_IDX=1 (checked here, locally, before dispatch).
  if [ -z "${FK_RESUME_ASSUME_IDX:-}" ]; then
    ssm_run <<'EOF'
[ -f /tmp/mig/.idx-applied ] || { echo "IDX_MARKER_MISSING - run load-tail instead (it applies indexes first), or verify indexes and set FK_RESUME_ASSUME_IDX=1"; exit 1; }
echo IDX_MARKER_OK
EOF
  fi
  prep_cnfs
  say "building the remaining-FK list from live constraint names, then applying it"
  ssm_run 21600 <<'EOF'
set -e; cd /tmp/mig
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --init-command='SET SESSION restrict_fk_on_non_standard_key=0'"
mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names \
  -e "SELECT CONCAT(constraint_schema,'.',constraint_name) FROM information_schema.referential_constraints" > applied-fks.txt
python3 - <<'PYEOF'
import re
applied = set(l.strip() for l in open('applied-fks.txt'))
# Known limit: the schema.constraint join key is ambiguous if an identifier
# itself contains a period. Pathological naming; a collision would skip a
# statement, so schemas with dotted identifiers are outside this phase's
# support - and load-tail is no fallback mid-FK (it replays the full file and
# dies on the first duplicate). Recover by hand there: find the last applied
# statement from the mariadb error position, trim deferred-fk.sql to the
# remainder, and apply the trimmed file directly.
# Anchored through "` FOREIGN KEY" so an identifier containing a doubled
# backtick fails the match and falls into the kept/unparsed bucket instead of
# being misread as a shorter name.
pat = re.compile(r'^ALTER TABLE `([^`]+)`\.`[^`]+` ADD CONSTRAINT `([^`]+)` FOREIGN KEY')
kept, skipped, unparsed = [], 0, 0
for line in open('deferred-fk.sql'):
    m = pat.match(line)
    if not m:
        # never silently drop a statement we cannot classify
        kept.append(line); unparsed += 1; continue
    if f"{m.group(1)}.{m.group(2)}" in applied:
        skipped += 1
    else:
        kept.append(line)
open('deferred-fk-remaining.sql','w').writelines(kept)
print(f"skipped={skipped} remaining={len(kept)} unparsed={unparsed}")
PYEOF
eval $T < deferred-fk-remaining.sql; touch .fk-applied; echo FK_APPLIED_ZERO_ORPHANS
EOF
  say "removing the FK-off Initstmt from the target endpoint (task stopped)"
  TEP=$(aws dms describe-endpoints --region "$REGION" --filters "Name=endpoint-id,Values=${ID}-aurora-migration-target" --query 'Endpoints[0].EndpointArn' --output text)
  aws dms modify-endpoint --endpoint-arn "$TEP" --extra-connection-attributes "" --region "$REGION" >/dev/null
  RIARN=$(aws dms describe-replication-instances --region "$REGION" --filters "Name=replication-instance-id,Values=${ID}-aurora-migration" --query 'ReplicationInstances[0].ReplicationInstanceArn' --output text)
  aws dms test-connection --replication-instance-arn "$RIARN" --endpoint-arn "$TEP" --region "$REGION" >/dev/null 2>&1 || true
  CWAIT=0
  while :; do
    cs=$(aws dms describe-connections --region "$REGION" --filters "Name=endpoint-arn,Values=$TEP" --query 'Connections[0].Status' --output text 2>/dev/null || echo none)
    [ "$cs" = "successful" ] && break
    [ "$cs" = "failed" ] && die "target endpoint connection test failed after Initstmt removal"
    CWAIT=$((CWAIT+1)); [ "$CWAIT" -gt 30 ] && die "target endpoint connection did not go successful in 5m (status=$cs)"
    sleep 10
  done
  say "resuming CDC (fresh sessions, FK checks ON)"
  aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
  while [ "$(task_status)" != "running" ]; do sleep 10; done
  say "load complete - CDC running. Soak, then 'validate'."
  ;;

validate)
  say "DMS validation states:"
  aws dms describe-table-statistics --replication-task-arn "$(task_arn)" --region "$REGION" --query 'TableStatistics[].ValidationState' --output text | tr '\t' '\n' | sort | uniq -c
  U=$(unclean_validation_count)
  [ "$U" = "0" ] || die "validation gate FAIL: $U tables outside the clean set (Validated / No primary key)"
  say "no-PK tables (classified exceptions):"
  aws dms describe-table-statistics --replication-task-arn "$(task_arn)" --region "$REGION" --query 'TableStatistics[?contains(ValidationState, `rimary`)].TableName' --output text | head -5
  if [ -n "${CHECKSUM_TABLES:-}" ]; then
    prep_cnfs
    say "checksums (source vs target) - writers should be quiet for a stable compare"
    for t in $CHECKSUM_TABLES; do
      R=$(ssm_run <<EOF
set -e
A=\$(mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "CHECKSUM TABLE $t" | awk '{print \$2}')
B=\$(mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names -e "CHECKSUM TABLE $t" | awk '{print \$2}')
# fail CLOSED: empty (connection/SQL failure) or NULL (nonexistent table) are
# errors, never matches.
case "\$A\$B" in (*NULL*|"") echo "INVALID src=\$A tgt=\$B"; exit 0;; esac
[ -n "\$A" ] && [ -n "\$B" ] && [ "\$A" = "\$B" ] && echo "MATCH \$A" || echo "MISMATCH src=\$A tgt=\$B"
EOF
)
      say "  $t: $(echo "$R" | tail -1)"; echo "$R" | grep -qE "MISMATCH|INVALID" && die "checksum gate failed on $t"
    done
  fi
  say "VALIDATION GATE PASS"
  ;;

harden)
  # Roll the STOP_TASK apply-error policies into the task's EFFECTIVE settings.
  # Terraform cannot: replication_task_settings is ignore_changes (create-time
  # only), so tasks provisioned before the policies landed keep the permissive
  # defaults (insert/update LOG_ERROR, delete IGNORE_RECORD) - under which a CDC
  # apply conflict is logged and skipped, and the fence's positional drain proof
  # would pass over a row that never landed. Modify requires a stopped task;
  # resume-processing continues CDC from the checkpoint afterwards.
  WANT="STOP_TASK,STOP_TASK,STOP_TASK,STOP_TASK"
  if [ "$(effective_apply_policies)" = "$WANT" ]; then say "apply-error policies already STOP_TASK - nothing to do"; exit 0; fi
  ST=$(task_status)
  case "$ST" in running|stopped) :;; *) die "task is '$ST' - harden expects running or stopped";; esac
  if [ "$ST" = "running" ]; then
    say "stopping the task to modify settings (CDC resumes from the checkpoint after)"
    aws dms stop-replication-task --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null
    wait_task_status stopped 900
  fi
  say "merging STOP_TASK apply-error policies into the effective settings"
  NEWSET=$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" \
    --query 'ReplicationTasks[0].ReplicationTaskSettings' --output text | \
    jq '.ErrorBehavior.ApplyErrorInsertPolicy = "STOP_TASK"
      | .ErrorBehavior.ApplyErrorUpdatePolicy = "STOP_TASK"
      | .ErrorBehavior.ApplyErrorDeletePolicy = "STOP_TASK"
      | .ErrorBehavior.ApplyErrorEscalationPolicy = "STOP_TASK"
      | del(.Logging.CloudWatchLogGroup, .Logging.CloudWatchLogStream)')
  aws dms modify-replication-task --replication-task-arn "$(task_arn)" --region "$REGION" \
    --replication-task-settings "$NEWSET" >/dev/null
  # modify transitions through "modifying"; wait for it to settle back
  sleep 15; wait_task_status stopped 600
  GOT=$(effective_apply_policies)
  [ "$GOT" = "$WANT" ] || die "effective policies after modify are '$GOT', wanted '$WANT'"
  if [ "$ST" = "running" ]; then
    say "resuming CDC"
    aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
    wait_task_status running 300
  fi
  say "HARDENED: effective apply-error policies are STOP_TASK"
  ;;

fence)
  say "PRECONDITIONS (yours): app writers scaled to zero + DDL freeze in effect"
  # The drain proof leans on the STOP_TASK apply-error policies (a conflict must
  # stop the task, never advance the checkpoint past a missing row). Terraform
  # only sets them at task creation - assert the EFFECTIVE settings.
  [ "$(effective_apply_policies)" = "STOP_TASK,STOP_TASK,STOP_TASK,STOP_TASK" ] || \
    die "the task's effective apply-error policies are not STOP_TASK - run the 'harden' phase first"
  # Fail fast, before the kill sweep touches anything. Re-checked immediately
  # before the apply prompt, since a backup can start during the steps between.
  preflight_fence_gate "entry"
  prep_cnfs
  # NOTE on the validation-epoch scope (see the epoch block after the drain
  # proof): the fresh task validates the main task's ENTIRE mapping scope (a
  # clone of its TableMappings), not a derived "touched during the fence"
  # subset. Every cheap touched-signal was tried and has a documented
  # false-negative hole: DMS table-statistics counters reset across task
  # lifecycle events, information_schema.tables.update_time is cached
  # (information_schema_stats_expiry, default 24h) and silently NULLed by
  # InnoDB dictionary-cache eviction, performance_schema is OFF on the fleet's
  # RDS sources (static parameter; enabling needs a reboot), and any catalog
  # fetched over SSM truncates at 24k chars of stdout. A missed table would be
  # a silent data-divergence pass, so the epoch validates everything the
  # migration migrated instead - minutes of extra fence time on a quiescent
  # pair, zero signal-validity risk.
  # BI task must stop BEFORE the cutover apply mutates its source endpoint
  # (DMS rejects endpoint modification on a running task).
  # Enumerate both ways rather than testing for "running": an unrecognised status must stop the
  # fence, not fall through as "nothing to stop". Getting this wrong repoints the endpoint under
  # a live replication.
  BIST=$(bi_task_status)
  case "$BIST" in
    running | starting | initializing | modifying)
      say "stopping the BI replication ($BIST) ahead of the endpoint repoint"
      bi_stop
      bi_wait_stopped 900
      ;;
    stopped | failed | created | not-started | deprovisioning | deprovisioned | none)
      say "BI replication needs no stop (status=$BIST)"
      ;;
    *) die "unrecognised BI replication status '$BIST' - refusing to fence with a replication in an unknown state" ;;
  esac
  # Re-entry detection: a previous fence attempt that died AFTER the Terraform
  # fence applied (e.g. mid-epoch failure) leaves the source read_only=1. The
  # marker step below writes to the source, so on a fenced source it would be
  # refused by the very fence it created (error 1290, hit live in production). On
  # re-entry: reuse the previous attempt's marker (reads still work) and skip
  # the sweep + marker write + interactive fence pause entirely - every proof
  # downstream (negative test, coordinate, marker gate, drain, epoch) re-runs.
  RO0=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT @@read_only"
EOF
)
  RO0=$(echo "$RO0" | tr -d '[:space:]')
  if [ "$RO0" = "1" ]; then
    say "source is ALREADY fenced (read_only=1) - re-entering a prior fence attempt"
    MARK=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT tag FROM aurora_mig_ctl.marker ORDER BY id DESC LIMIT 1" 2>/dev/null
EOF
)
    MARK=$(echo "$MARK" | tr -d '[:space:]' | grep . | head -1) || true
    [ -n "$MARK" ] || die "re-entry: no marker found on the fenced source - cannot anchor the epoch; unfence and start over"
    say "  reusing marker '$MARK' from the prior attempt"
  else
  MARK="cutover-$(date -u +%s)"
  say "kill sweep (by connection id - app and runner share the master user) + marker '$MARK'"
  ssm_run <<EOF
set -e
M="mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names"
\$M <<'SQL'
CREATE DATABASE IF NOT EXISTS aurora_mig_ctl;
CREATE TABLE IF NOT EXISTS aurora_mig_ctl.marker (id INT AUTO_INCREMENT PRIMARY KEY, tag VARCHAR(64), at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6));
SQL
# one CALL per session id; sweep repeatedly until no foreign sessions remain
# (each sweep's SELECT excludes its own momentary connection + rdsadmin + the
# DMS binlog reader, which must keep streaming until the final stop)
for sweep in 1 2 3; do
  IDS=\$(\$M -e "SELECT id FROM information_schema.processlist WHERE id != CONNECTION_ID() AND user NOT IN ('rdsadmin') AND command != 'Binlog Dump'")
  [ -z "\$IDS" ] && break
  for i in \$IDS; do \$M -e "CALL mysql.rds_kill(\$i);" 2>/dev/null || true; done
  sleep 2
done
LEFT=\$(\$M -e "SELECT COUNT(*) FROM information_schema.processlist WHERE id != CONNECTION_ID() AND user NOT IN ('rdsadmin') AND command != 'Binlog Dump'")
# Best-effort with a LIVE app: its connection pool reconnects between sweeps, so
# zero is unreachable in a fence-only cutover (writers are NOT scaled to zero).
# Correctness does not depend on it - the write cutoff is read_only=1 itself.
# Anything a lingering session commits before the fence lands in the binlog
# BEFORE the final coordinate (captured post-fence), so the checkpoint drain
# proof covers it; the post-fence sweep + negative test are the hard guarantees.
echo "foreign_sessions_remaining=\$LEFT (best-effort; fence is the cutoff)"
mariadb --defaults-extra-file=/tmp/mig-src.cnf -e "INSERT INTO aurora_mig_ctl.marker (tag) VALUES ('$MARK');"
echo MARKER_COMMITTED
EOF
  # The load-bearing check: this is the apply that gets deferred. The entry check
  # can be minutes stale by now (BI stop, kill sweep, marker commit all sit
  # between them) and a backup starting in that gap is exactly the 262.89s case.
  preflight_fence_gate "pre-apply"
  say ""
  say ">>> NOW APPLY THE TERRAFORM FENCE: set aurora_migration_source_fenced = true"
  say ">>> in this env's env.hcl and confirm the gated physical apply, then return."
  say "Polling for read_only=1..."
  while :; do RO=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT @@read_only"
EOF
); RO=$(echo "$RO" | tr -d '[:space:]'); say "  read_only=$RO"; [ "$RO" = "1" ] && break; sleep 30; done
  fi
  say "post-fence kill sweep (anything that connected during the apply window;"
  say "read_only already blocks their writes, this just tidies sessions)"
  ssm_run <<'EOF'
M="mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names"
for i in $($M -e "SELECT id FROM information_schema.processlist WHERE id != CONNECTION_ID() AND user NOT IN ('rdsadmin') AND command != 'Binlog Dump'"); do $M -e "CALL mysql.rds_kill($i);" 2>/dev/null || true; done
echo SWEPT
EOF
  say "negative write test (must be refused)"
  ssm_run <<'EOF'
if mariadb --defaults-extra-file=/tmp/mig-src.cnf -e "INSERT INTO aurora_mig_ctl.marker (tag) VALUES ('must-fail');" 2>/tmp/neg.err; then echo FENCE_BREACH; exit 1; else echo FENCE_OK; fi
EOF
  say "capturing the FINAL source binlog coordinate (nothing can write past it now)"
  COORD=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SHOW MASTER STATUS" | awk '{print $1, $2}'
EOF
)
  FINAL_FILE=$(echo "$COORD" | awk '{print $1}' | grep -oE '[0-9]+$'); FINAL_POS=$(echo "$COORD" | awk '{print $2}' | tr -d '[:space:]')
  [ -n "$FINAL_FILE" ] && [ -n "$FINAL_POS" ] || die "could not capture the final binlog coordinate"
  say "final coordinate: file-seq=$FINAL_FILE pos=$FINAL_POS"
  say "gate: marker on Aurora"
  # On re-entry the main task may be stopped with the marker not yet replicated
  # (a prior attempt that died between the fence apply and the drain proof) -
  # CDC must run for the marker to cross. Resume ONLY from a clean "stopped"
  # (start-replication-task on a transitional state throws
  # InvalidResourceStateFault, and a task felled by the STOP_TASK error policy
  # must never be blindly resumed). Wall-clock bounded via SECONDS so nested
  # waits and slow SSM round-trips count against the deadline.
  MDEADLINE=$((SECONDS + 900))
  while :; do
    # deadline gates STARTING new work: a blocking probe (ssm_run can run long)
    # that begins before the deadline may finish after it, and a late success
    # is still a success - but no new probe/resume ever starts past it.
    [ "$SECONDS" -lt "$MDEADLINE" ] || die "marker did not reach Aurora within the 15m deadline - inspect the main task's CDC"
    N=$(ssm_run <<EOF
mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names -e "SELECT COUNT(*) FROM aurora_mig_ctl.marker WHERE tag='$MARK'" 2>/dev/null || echo 0
EOF
); N=$(echo "$N" | tr -d '[:space:]'); say "  marker_on_aurora=$N"; [ "$N" = "1" ] && break
    # re-check after the probe: a probe that started before the deadline but
    # failed after it must not go on to start a resume
    [ "$SECONDS" -lt "$MDEADLINE" ] || die "marker did not reach Aurora within the 15m deadline - inspect the main task's CDC"
    S=$(task_status)
    case "$S" in
      stopped)
        say "  marker not on Aurora and the task is stopped - resuming CDC to replicate it"
        MARN=$(task_arn)
        # final gate immediately before the mutation: the describe calls above
        # block too, and the resume commits us to up to 300s of waiting
        [ "$SECONDS" -lt "$MDEADLINE" ] || die "marker did not reach Aurora within the 15m deadline - inspect the main task's CDC"
        aws dms start-replication-task --replication-task-arn "$MARN" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
        wait_task_status running 300
        ;;
      running|starting|stopping|modifying) : ;; # in flight - poll again
      *) die "task is '$S' while waiting for the marker to replicate - inspect it before continuing" ;;
    esac
    sleep 10
  done
  # Drain proof - STOP FIRST, then assert the checkpoint. RecoveryCheckpoint is a
  # stopped-task safepoint: it advances to the true applied position only when the
  # task stops, NOT while it runs. The old code asserted checkpoint >= coordinate
  # while the task ran, which can never pass once the source's filtered-schema
  # tail (rds_heartbeat and other mysql-schema system writes DMS excludes) rotates
  # the binlog past the last applied real event - the checkpoint parks at the last
  # consumable event and the app stays fenced until the 15m timeout. So: stop,
  # read the flushed checkpoint, and if a genuine backlog left it short of the
  # fenced source's final coordinate, resume to drain and re-stop. Bounded, and
  # every state transition is a bounded wait (a fenced source must never hang on
  # a task that silently failed).
  FINAL_FILE=$((10#$FINAL_FILE))
  say "task stop (flushes RecoveryCheckpoint to the true applied position)"
  # tolerate re-entry states: already stopped (prior drain-proof stop), already
  # stopping (don't double-issue stop - InvalidResourceStateFault), or starting
  # (wait for running before a stop is accepted)
  case "$(task_status)" in
    stopped) : ;;
    stopping) wait_task_status stopped 900 ;;
    starting)
      wait_task_status running 300
      aws dms stop-replication-task --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null
      wait_task_status stopped 900
      ;;
    *)
      aws dms stop-replication-task --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null
      wait_task_status stopped 900
      ;;
  esac
  say "gate: post-stop checkpoint at or past the final coordinate (the drain proof)"
  CKTRIES=0
  while :; do
    if checkpoint_reached "$FINAL_FILE" "$FINAL_POS"; then say "  checkpoint reached (post-stop)"; break; fi
    CKTRIES=$((CKTRIES+1)); [ "$CKTRIES" -gt 10 ] && die "post-stop checkpoint short of the final coordinate after 10 resume/stop cycles - inspect the task (RecoveryCheckpoint: $(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].RecoveryCheckpoint' --output text); final=$FINAL_FILE:$FINAL_POS)"
    say "  checkpoint short of final - resuming to drain the backlog, then re-stopping (cycle $CKTRIES)"
    aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
    wait_task_status running 300
    sleep 45
    aws dms stop-replication-task --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null
    wait_task_status stopped 900
  done
  # Final validation epoch, AFTER the drain - a FRESH validation-only DMS task
  # over the main task's entire mapping scope. Why fresh: the main task's
  # validation is asynchronous with no resume barrier and no epoch token, so
  # ANY observation of its state can be stale. A fresh task has no history -
  # every selected table starts unvalidated in THIS epoch, so Validated on it is
  # necessarily epoch-new, with an unambiguous completion boundary. Both sides
  # are quiescent (source fenced, main task stopped after the drain proof), so
  # it is a clean full row compare. Scope = a CLONE of the main task's own
  # TableMappings: the mappings that DEFINE what was migrated. Against the
  # frozen source the fresh task enumerates exactly the migrated table set -
  # no operator-side catalog derivation at all. (Every derived-catalog path
  # failed adversarial review: DMS counters reset, update_time is cached and
  # eviction-NULLed, performance_schema is off on the fleet, and an SSM-fetched
  # catalog silently truncates at 24k chars of stdout. The mappings clone has
  # no such channel: it travels via DMS APIs with real pagination.)
  # Completion requires EXACT set equality with the main task's statistics
  # identities in both directions (count alone can hide an equal-cardinality
  # substitution from rename/DDL outside CDC), every main-set table terminal in
  # {Validated, No primary key} (no-PK rows reported - the documented
  # users-table exception), and the marker table's row Validated: that row
  # exists only if THIS fence's write replicated, so the epoch is self-proving.
  say "gate: final validation epoch over the drained dataset (fresh validation-only task, mappings-clone scope)"
  VTASK_ID="${ID}-aurora-migration-fencevalidate"
  vtask_arn() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$VTASK_ID" --query 'ReplicationTasks[0].ReplicationTaskArn' --output text 2>/dev/null | grep -v '^None$' || true; }
  vtask_status() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$VTASK_ID" --query 'ReplicationTasks[0].Status' --output text 2>/dev/null || echo none; }
  vwait_status() { # $1=wanted $2=timeout seconds - bounded, dies on failure states
    local waited=0 s
    while :; do
      s=$(vtask_status)
      [ "$s" = "$1" ] && return 0
      case "$s" in failed|stopped-after-fail|deleting) [ "$1" = "deleting" ] || die "validation task entered '$s' while waiting for '$1'";; esac
      [ "$waited" -ge "$2" ] && die "validation task did not reach '$1' in ${2}s (status=$s)"
      sleep 10; waited=$((waited+10))
    done
  }
  vtask_teardown() { # stop if running, delete, wait gone - every wait bounded
    local arn; arn=$(vtask_arn); [ -n "$arn" ] || return 0
    [ "$(vtask_status)" = "running" ] && { aws dms stop-replication-task --replication-task-arn "$arn" --region "$REGION" >/dev/null; vwait_status stopped 900; }
    aws dms delete-replication-task --replication-task-arn "$arn" --region "$REGION" >/dev/null
    local waited=0
    while [ -n "$(vtask_arn)" ]; do
      [ "$waited" -ge 600 ] && die "validation task did not delete in 600s"
      sleep 10; waited=$((waited+10))
    done
  }
  if [ -n "$(vtask_arn)" ]; then
    say "  deleting a leftover fence-validation task from a prior attempt"
    vtask_teardown
  fi
  # The mappings clone + all large JSON travel via FILES, never argv (a large
  # mapping can exceed ARG_MAX and would kill the fence mid-read-only window).
  VDIR=$(mktemp -d "/tmp/fence-epoch-${ID}.XXXXXX")
  aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" \
    --query 'ReplicationTasks[0].TableMappings' --output text > "$VDIR/mappings.json"
  jq -e '.rules | length >= 1' "$VDIR/mappings.json" >/dev/null || die "could not clone the main task's TableMappings - do not trust this epoch"
  # the authoritative table SET: the main task's own statistics identities
  # (stable on a stopped task; survives the stop - proven live on a
  # production cutover). The completion gate requires exact set equality with the fresh
  # task's enumeration, not count equality - equal counts can hide an
  # equal-cardinality substitution (e.g. a rename DDL that MySQL CDC does not
  # replicate leaves the two enumerations different but same-sized).
  # NO --query aggregates/projections here: JMESPath evaluates PER PAGE under
  # CLI pagination (observed live: length() returned one line per 100-row
  # page). jq -s normalizes one merged doc or one doc per page alike.
  aws dms describe-table-statistics --replication-task-arn "$(task_arn)" --region "$REGION" \
    --output json | jq -s '[.[].TableStatistics[] | {s: .SchemaName, t: .TableName}]' > "$VDIR/mainset.json"
  MAIN_COUNT=$(jq 'length' "$VDIR/mainset.json")
  [ "$MAIN_COUNT" -ge 1 ] 2>/dev/null || die "main task reports no table statistics - cannot anchor the epoch's expected table set"
  say "  scope: main-task mappings clone; expected table set size $MAIN_COUNT"
  RIARN=$(aws dms describe-replication-instances --region "$REGION" --filters "Name=replication-instance-id,Values=${ID}-aurora-migration" --query 'ReplicationInstances[0].ReplicationInstanceArn' --output text)
  SEP=$(aws dms describe-endpoints --region "$REGION" --filters "Name=endpoint-id,Values=${ID}-aurora-migration-source" --query 'Endpoints[0].EndpointArn' --output text)
  TEP=$(aws dms describe-endpoints --region "$REGION" --filters "Name=endpoint-id,Values=${ID}-aurora-migration-target" --query 'Endpoints[0].EndpointArn' --output text)
  say "  creating the validation-only task ($VTASK_ID)"
  VARN=$(aws dms create-replication-task --region "$REGION" \
    --replication-task-identifier "$VTASK_ID" \
    --replication-instance-arn "$RIARN" \
    --source-endpoint-arn "$SEP" --target-endpoint-arn "$TEP" \
    --migration-type full-load \
    --table-mappings "file://$VDIR/mappings.json" \
    --replication-task-settings '{"FullLoadSettings":{"TargetTablePrepMode":"DO_NOTHING"},"ValidationSettings":{"EnableValidation":true,"ValidationOnly":true,"ThreadCount":16,"FailureMaxCount":10000},"Logging":{"EnableLogging":true}}' \
    --query 'ReplicationTask.ReplicationTaskArn' --output text)
  vwait_status ready 600
  aws dms start-replication-task --replication-task-arn "$VARN" --start-replication-task-type start-replication --region "$REGION" >/dev/null
  say "  validating (every enumerated table must reach Validated or No-primary-key in this epoch)"
  VWAIT=0
  while :; do
    # same pagination rule: raw json + jq -s, never --query aggregates/projections
    aws dms describe-table-statistics --replication-task-arn "$VARN" --region "$REGION" \
      --output json | jq -s '[.[].TableStatistics[] | {s: .SchemaName, t: .TableName, v: .ValidationState}]' > "$VDIR/vstats.json"
    # terminal-bad states die immediately; "no primary key" is the accepted,
    # reported exception (those tables cannot be row-validated at all)
    NBAD=$(jq '[.[] | select(.v | test("mismatch|error|suspend"; "i"))] | length' "$VDIR/vstats.json")
    # exact SET equality with the main task's identities, both directions:
    # MISSING = main-set tables not yet terminal-clean in the fresh task;
    # EXTRA = fresh-task tables outside the main set (enumeration drift - the
    # fresh enumeration is fixed at its start, so EXTRA never resolves: die).
    MISSING=$(jq -n --slurpfile main "$VDIR/mainset.json" --slurpfile now "$VDIR/vstats.json" '
      ($now[0] | map(select((.v | ascii_downcase) == "validated" or (.v | ascii_downcase) == "no primary key")
                     | {key: ([.s, .t] | tojson), value: true}) | from_entries) as $ok
      | [ $main[0][] | select(($ok[[.s, .t] | tojson] // false) | not) ] | length')
    EXTRA=$(jq -n --slurpfile main "$VDIR/mainset.json" --slurpfile now "$VDIR/vstats.json" '
      ($main[0] | map({key: ([.s, .t] | tojson), value: true}) | from_entries) as $known
      | [ $now[0][] | select(($known[[.s, .t] | tojson] // false) | not) ] | length')
    MARKER_OK=$(jq '[.[] | select(.s == "aurora_mig_ctl" and .t == "marker" and ((.v | ascii_downcase) == "validated"))] | length' "$VDIR/vstats.json")
    say "  missing=$MISSING/$MAIN_COUNT extra=$EXTRA bad=$NBAD marker_validated=$MARKER_OK"
    [ "$NBAD" -gt 0 ] && die "fence-epoch validation found non-clean tables - inspect $VTASK_ID table statistics before touching anything"
    [ "$EXTRA" -gt 0 ] && die "the fresh task enumerated tables outside the main task's set - enumeration drift (rename/DDL outside CDC?); inspect before touching anything"
    if [ "$MISSING" = "0" ] && [ "$MARKER_OK" = "1" ]; then break; fi
    # Deadline scales with table count. VWAIT is NOT seconds: it adds 20 per
    # loop but each loop's paginated describe-table-statistics takes ~40s of
    # wall clock on a 10k+ table env, so real time is roughly VWAIT x2. The
    # old fixed 3600 (~2h wall) barely fit an 11.9k-table env and cannot fit
    # a 24k-table one; a miss restarts the whole epoch inside the write
    # freeze. Observed rate is ~0.25 VWAIT units per table, so count/2 +1800
    # leaves >2x headroom while still bounding a genuine hang.
    VMAX=$((MAIN_COUNT / 2 + 1800))
    VWAIT=$((VWAIT+20)); [ "$VWAIT" -gt "$VMAX" ] && die "fence-epoch validation did not complete (VWAIT=$VWAIT > $VMAX, ~2s wall per unit; missing=$MISSING/$MAIN_COUNT, marker=$MARKER_OK) - inspect $VTASK_ID"
    sleep 20
  done
  NOPK_LIST=$(jq -r '[.[] | select((.v | ascii_downcase) == "no primary key") | .s + "." + .t] | join(" ")' "$VDIR/vstats.json")
  [ -z "$NOPK_LIST" ] || say "  no-PK tables outside row validation (documented exception): $NOPK_LIST"
  say "  epoch validation PASSED - removing the validation task"
  vtask_teardown
  rm -rf "$VDIR"
  # main-task whole-task whitelist gate (its stats survive the stop): catches a
  # table that went Mismatched/Suspended during soak.
  U=$(unclean_validation_count)
  [ "$U" = "0" ] || die "main-task validation states are not clean (unclean=$U) - inspect before promoting"
  say "checkpoint: $(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].RecoveryCheckpoint' --output text)"
  say "installing object artifacts AFTER the final stop (DMS can never apply DML"
  say "through freshly installed triggers). Dumped FRESH from the fenced source -"
  say "consistent by construction, immune to bastion replacement losing /tmp."
  ssm_run <<'EOF'
set -e; umask 177
S="mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names"
$S -e "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('mysql','information_schema','performance_schema','sys','innodb','tmp','aurora_mig_ctl') AND schema_name NOT LIKE 'awsdms%'" > /tmp/fence-schemas.txt
mariadb-dump --defaults-extra-file=/tmp/mig-src.cnf --no-data --no-create-info --no-create-db --routines --events --triggers --skip-lock-tables --no-tablespaces --databases $(tr '\n' ' ' < /tmp/fence-schemas.txt) > /tmp/fence-objects.sql
SRC_COUNTS="$($S -e "SELECT CONCAT((SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema NOT IN ('mysql','sys')),'/',(SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema NOT IN ('mysql','sys')),'/',(SELECT COUNT(*) FROM information_schema.events WHERE event_schema NOT IN ('mysql','sys')))")"
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names"
if [ "$SRC_COUNTS" != "0/0/0" ]; then
  mariadb --defaults-extra-file=/tmp/mig-tgt.cnf < /tmp/fence-objects.sql
fi
# the src-vs-tgt comparison ALWAYS runs: with a 0/0/0 source it proves no stale
# objects linger on the target from an earlier aborted attempt.
TGT_COUNTS="$($T -e "SELECT CONCAT((SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema NOT IN ('mysql','sys')),'/',(SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema NOT IN ('mysql','sys')),'/',(SELECT COUNT(*) FROM information_schema.events WHERE event_schema NOT IN ('mysql','sys')))")"
[ -n "$TGT_COUNTS" ] || { echo "OBJECT_COUNT_UNREADABLE"; exit 1; }
[ "$SRC_COUNTS" = "$TGT_COUNTS" ] && echo "OBJECTS_VERIFIED $SRC_COUNTS" || { echo "OBJECT_COUNT_MISMATCH src=$SRC_COUNTS tgt=$TGT_COUNTS"; exit 1; }
EOF
  say ""
  say "GO: the gate has passed. Confirming the aurora_migration_state=cutover"
  say "physical apply IS the promotion - its auto-following logical apply"
  say "repoints the app at Aurora (the point of no return). After the logical"
  say "apply completes, run 'cutover-verify'. To stand down instead, run 'abort'."
  ;;

cutover-verify)
  # Post-promotion verification. The logical stack has already repointed the app.
  prep_cnfs
  ssm_run <<'EOF'
set -e
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names"
$T -e "SELECT @@aurora_version" | grep -q "8.4" || { echo WRONG_TARGET; exit 1; }
echo "aurora identity OK: $($T -e 'SELECT @@aurora_server_id')"
$T -e "INSERT INTO aurora_mig_ctl.marker (tag) VALUES ('post-promotion-verify');" && echo AURORA_WRITE_OK
echo "event_scheduler=$($T -e 'SELECT @@event_scheduler') (expect ON post-cutover; OFF means the provision-phase param is still active - re-check the cutover apply)"
echo "objects on target: routines=$($T -e "SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema NOT IN ('mysql','sys')") triggers=$($T -e "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema NOT IN ('mysql','sys')") events=$($T -e "SELECT COUNT(*) FROM information_schema.events WHERE event_schema NOT IN ('mysql','sys')")"
EOF
  say "compare object counts to preload's; verify app health, then schedule 'bi-epoch' and later cleanup."
  ;;

abort)
  # Hard, fail-closed guards: abort is PRE-CUTOVER ONLY. Cutover cannot be
  # in-flight concurrently: the fence-required-at-cutover Terraform validation
  # means an unfence apply and a cutover apply are mutually exclusive changes
  # to the same gated stack.
  H=$(primary_secret_host) || die "REFUSED: cannot read the primary secret (fail closed)"
  A=$(aurora_endpoint) || die "REFUSED: cannot resolve the Aurora endpoint (fail closed)"
  [ -n "$H" ] && [ -n "$A" ] || die "REFUSED: empty endpoint facts (fail closed)"
  [ "$H" = "$A" ] && die "REFUSED: primary secret host already points at Aurora - cutover has been applied. Fail forward."
  prep_cnfs
  PW=$(ssm_run <<'EOF'
set -e
mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names -e "SELECT COUNT(*) FROM aurora_mig_ctl.marker WHERE tag LIKE 'post-promotion%'"
EOF
) || die "REFUSED: cannot query Aurora for promotion evidence (fail closed)"
  PW=$(echo "$PW" | tr -d '[:space:]')
  [ "$PW" = "0" ] || die "REFUSED: post-promotion writes exist on Aurora. Fail forward."
  say "removing any objects the fence installed on the target (triggers must not"
  say "fire on replicated DML when CDC resumes for a retry)"
  ssm_run <<'EOF'
set -e
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names"
$T -e "SELECT CONCAT('DROP TRIGGER IF EXISTS \`',trigger_schema,'\`.\`',trigger_name,'\`;') FROM information_schema.triggers WHERE trigger_schema NOT IN ('mysql','sys')" > /tmp/drop-objs.sql
$T -e "SELECT CONCAT('DROP EVENT IF EXISTS \`',event_schema,'\`.\`',event_name,'\`;') FROM information_schema.events WHERE event_schema NOT IN ('mysql','sys')" >> /tmp/drop-objs.sql
if [ -s /tmp/drop-objs.sql ]; then mariadb --defaults-extra-file=/tmp/mig-tgt.cnf < /tmp/drop-objs.sql && echo TARGET_OBJECTS_DROPPED; else echo NO_TARGET_OBJECTS; fi
EOF
  # An unfence issued while the FENCE apply is still in flight is REFUSED by RDS:
  # InvalidDBParameterGroupState ("this parameter group cannot be modified
  # because it is currently being applied"). Reproduced deterministically
  # 2026-08-02. On a quiet instance the lock clears in ~0.7s and nobody notices;
  # behind a backup it lasts as long as the backup does (measured 262.89s), and
  # for that whole window the fence is UNABORTABLE. Report the state rather than
  # letting the operator meet the error as a mystery Terraform failure.
  PGS=$(aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" \
        --query 'DBInstances[0].DBParameterGroups[0].ParameterApplyStatus' --output text 2>/dev/null || echo unknown)
  if [ "$PGS" != in-sync ]; then
    say "WARNING: source parameter group is '$PGS', not in-sync. An unfence apply issued now"
    say "         will be REFUSED with InvalidDBParameterGroupState. This is expected and"
    say "         self-clearing - wait for in-sync (seconds normally, the length of a backup"
    say "         if one is running) and apply then. The fence stays up until you do."
    say "         Watch it: aws rds describe-db-instances --db-instance-identifier $SRC_ID \\"
    say "           --region $REGION --query 'DBInstances[0].DBParameterGroups[0].ParameterApplyStatus'"
  else
    say "source parameter group is in-sync - an unfence apply will be accepted"
  fi
  say ">>> UNFENCE: set aurora_migration_source_fenced = false in env.hcl and"
  say ">>> confirm the gated apply (Terraform refuses this while state=cutover)."
  say "Polling for read_only=0..."
  while :; do RO=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT @@read_only"
EOF
); RO=$(echo "$RO" | tr -d '[:space:]'); say "  read_only=$RO"; [ "$RO" = "0" ] && break; sleep 30; done
  # re-verify the guard AFTER the human-gated apply window (TOCTOU close-out).
  # Every fact is captured with an explicit error check: an unreadable fact is a
  # refusal, never a pass.
  H2=$(primary_secret_host) || die "post-unfence verification failed reading the secret (fail closed)"
  A2=$(aurora_endpoint) || die "post-unfence verification failed resolving Aurora (fail closed)"
  [ -n "$H2" ] && [ "$H2" != "null" ] && [ -n "$A2" ] && [ "$A2" != "null" ] || die "post-unfence verification got empty facts (fail closed)"
  [ "$H2" = "$A2" ] && die "INCONSISTENT: secret now points at Aurora after unfence - investigate before touching writers"
  if [ "$(task_status)" = "stopped" ]; then
    say "re-establishing CDC from the checkpoint (so the migration can retry later)"
    aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
  fi
  if [ "$(bi_task_status)" = "stopped" ]; then
    say "resuming the BI task against the (still RDS) source"
    bi_start resume-processing
  fi
  say "ABORTED cleanly: source writable, Aurora untouched by the app, CDC re-established. Scale writers back up."
  ;;

bi-epoch)
  # reload-target needs the replication not running. Enumerated rather than written as a
  # `stopped || running &&` chain: that form also exited non-zero (and so, under set -e, died
  # with no message) for every other status, and it silently accepted "none".
  ST=$(bi_task_status)
  case "$ST" in
    running | starting | initializing | modifying)
      say "stopping the BI replication ($ST) before the reload"
      bi_stop
      bi_wait_stopped 900
      ;;
    stopped | failed | created | not-started) ;;
    none) die "no BI replication exists for $ID - nothing to re-epoch" ;;
    deprovisioning | deprovisioned)
      die "the BI serverless replication is $ST (stopped or failed for 48h) - it cannot be resumed; recreate it with a physical apply, then re-run bi-epoch"
      ;;
    *) die "unrecognised BI replication status '$ST' - refusing to reload" ;;
  esac
  say "reloading the BI target from the (now Aurora) source"
  bi_start reload-target
  say "BI task reloading (its source endpoint followed the flipped db host at the cutover apply)"
  ;;

*) die "unknown phase: $PHASE" ;;
esac
