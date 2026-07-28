#!/usr/bin/env bash
# RDS -> Aurora Serverless v2 migration runner. Drives the DB-side phases the
# aurora_migration_state Terraform (aurora-migration.tf) cannot: native schema
# pre-load, DMS task orchestration, validation gates, and the fence/cutover
# sequence. Every step is the 2026-07-26 gca-rehearsal procedure, productionized
# (design doc: 3m-aurora-dms-migration-design; review round 2 fixes folded in).
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
set -euo pipefail

ID="${1:?identifier (e.g. m3-gca)}"; REGION="${2:?region}"; PHASE="${3:?phase}"
SRC_ID="$ID"; CLUSTER_ID="$ID"; TASK_ID="${ID}-aurora-migration"

say() { printf '%s %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
die() { say "FATAL: $*" >&2; exit 1; }

# --- discovery ---------------------------------------------------------------
bastion_id() {
  # Pin the Name tag to "<id>-bastion", NOT tag:Environment. A 3M account keeps
  # both the new Dozuki-managed (m3-*) and the legacy (mmm-*) bastion, and BOTH
  # carry Environment=<env>. Matching on Environment alone grabs whichever
  # Reservations[0] happens to be first - often the legacy bastion, which sits
  # in a different VPC and cannot route to the m3 RDS (connect times out).
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
bi_task_arn() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$ID" --query 'ReplicationTasks[0].ReplicationTaskArn' --output text 2>/dev/null; }
bi_task_status() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$ID" --query 'ReplicationTasks[0].Status' --output text 2>/dev/null || echo none; }
# NOTE: the projection query has NO `| [0]`. A `| [0]` inside --query is a
# pipe-expression the AWS CLI evaluates PER PAGE of paginated list-secrets
# results, so a page without a match emits "None" and the match on a later page
# emits the ARN - the caller then sees "None" first and the whole chain fails.
# The bare projection aggregates across every page; we pick the first real ARN.
first_secret_arn() { # $1 = name prefix
  aws secretsmanager list-secrets --region "$REGION" --query "SecretList[?starts_with(Name, '$1')].ARN" --output text | tr '\t' '\n' | grep -v '^None$' | grep . | head -1
}
primary_secret() { first_secret_arn "${ID}-database"; }
migration_secret() { first_secret_arn "${ID}-aurora-migration"; }
src_pg() { aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" --query 'DBInstances[0].DBParameterGroups[0].DBParameterGroupName' --output text; }
primary_secret_host() { aws secretsmanager get-secret-value --region "$REGION" --secret-id "$(primary_secret)" --query SecretString --output text | jq -r .host; }

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
ssm_run() {
  local script cid status
  script="$(cat)"
  cid=$(aws ssm send-command --region "$REGION" --instance-ids "$(bastion_id)" \
        --document-name AWS-RunShellScript --timeout-seconds 5400 \
        --parameters "$(jq -n --arg c "$script" '{commands: [$c]}')" \
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
case "$PHASE" in

status)
  say "migration task: $(task_status)   bi task: $(bi_task_status)"
  say "rds: $(rds_endpoint 2>/dev/null || echo none)"
  say "aurora: $(aurora_endpoint 2>/dev/null || echo none)"
  say "primary secret host: $(primary_secret_host 2>/dev/null || echo unknown)  (aurora after cutover)"
  ;;

preload)
  prep_cnfs
  ssm_run <<'EOF'
set -e; cd /tmp; rm -rf mig && mkdir mig && cd mig
S="mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names"
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names --init-command='SET SESSION restrict_fk_on_non_standard_key=0'"
$S -e "SELECT @@binlog_format, @@binlog_row_image, @@log_bin" | grep -q "ROW	FULL	1" || { echo "SOURCE NOT CDC-READY"; exit 1; }
[ "$($S -e 'SELECT @@lower_case_table_names')" = "$(eval $T -e "'SELECT @@lower_case_table_names'")" ] || { echo LCTN_MISMATCH; exit 1; }
$S -e "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('mysql','information_schema','performance_schema','sys','innodb','tmp') AND schema_name NOT LIKE 'awsdms%'" > schemas.txt
# dangling FKs: unenforceable residue, excluded from the target and reported
$S -e "SELECT CONCAT(k.constraint_schema,'.',k.table_name,'.',k.constraint_name) FROM information_schema.key_column_usage k LEFT JOIN information_schema.tables t ON t.table_schema=k.referenced_table_schema AND t.table_name=k.referenced_table_name WHERE k.referenced_table_name IS NOT NULL AND t.table_name IS NULL" > dangling-fks.txt
echo "dangling FKs: $(wc -l < dangling-fks.txt)"; cat dangling-fks.txt
# object artifacts: routines/events/triggers are NOT migrated by DMS and NOT in
# base DDL. Dumped here; installed on the target by fence (after the final task
# stop, so triggers can never fire on DMS-applied DML). Empty on the 3M fleet.
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
  ssm_run <<'EOF'
set -e; cd /tmp/mig
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --init-command='SET SESSION restrict_fk_on_non_standard_key=0'"
eval $T < deferred-idx.sql; echo IDX_APPLIED
eval $T < deferred-fk.sql;  echo FK_APPLIED_ZERO_ORPHANS
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
  prep_cnfs
  # Epoch anchor for the final revalidation gate: the SOURCE server's own clock,
  # captured before anything in this fence commits (marker included). The
  # touched set is later derived from information_schema.tables.update_time on
  # the fenced source - engine truth, immune to DMS task-statistics counters
  # resetting across task stop/resume cycles.
  say "capturing the source epoch anchor (for the touched-table derivation)"
  SRC_EPOCH=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT NOW(6)"
EOF
)
  SRC_EPOCH=$(echo "$SRC_EPOCH" | tr -d '\r' | grep . | head -1)
  [ -n "$SRC_EPOCH" ] || die "could not capture the source epoch anchor"
  say "  source epoch: $SRC_EPOCH"
  # BI task must stop BEFORE the cutover apply mutates its source endpoint
  # (DMS rejects endpoint modification on a running task).
  if [ "$(bi_task_status)" = "running" ]; then
    say "stopping the BI DMS task ahead of the endpoint repoint"
    aws dms stop-replication-task --replication-task-arn "$(bi_task_arn)" --region "$REGION" >/dev/null
    while [ "$(bi_task_status)" != "stopped" ]; do sleep 10; done
  fi
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
  say ""
  say ">>> NOW APPLY THE TERRAFORM FENCE: set aurora_migration_source_fenced = true"
  say ">>> in this env's env.hcl and confirm the gated physical apply, then return."
  say "Polling for read_only=1..."
  while :; do RO=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT @@read_only"
EOF
); RO=$(echo "$RO" | tr -d '[:space:]'); say "  read_only=$RO"; [ "$RO" = "1" ] && break; sleep 30; done
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
  while :; do N=$(ssm_run <<EOF
mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names -e "SELECT COUNT(*) FROM aurora_mig_ctl.marker WHERE tag='$MARK'" 2>/dev/null || echo 0
EOF
); N=$(echo "$N" | tr -d '[:space:]'); say "  marker_on_aurora=$N"; [ "$N" = "1" ] && break; sleep 10; done
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
  aws dms stop-replication-task --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null
  wait_task_status stopped 900
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
  # scoped to the tables the fence window touched. Why fresh: the main task's
  # validation is asynchronous with no resume barrier and no epoch token, so
  # ANY observation of its state can be stale. A fresh task has no history -
  # every selected table starts unvalidated in THIS epoch, so Validated on it is
  # necessarily epoch-new, with an unambiguous completion boundary. Both sides
  # are quiescent (source fenced, main task stopped after the drain proof), so
  # it is a clean full row compare. The touched set comes from the SOURCE
  # engine's information_schema.tables.update_time against the fence-start
  # epoch anchor - NOT from DMS table-statistics counters, which reset across
  # task stop/resume and would silently drop a baseline-zero table from the
  # set. Partitioned tables (update_time is NULL for them) are included
  # unconditionally - a safe superset. Tables without a PK/UNIQUE key cannot be
  # row-validated and are excluded (the pre-existing documented exception).
  # aurora_mig_ctl.marker is always in the set (its insert is part of this
  # fence and it has a PK), so the machinery self-canaries. FK-cascaded child
  # rows are not binlogged by MySQL and so not in the set; identical FK DDL on
  # both sides (preload md5 gate) makes their outcomes equal by construction.
  say "gate: final validation epoch over the drained dataset (fresh validation-only task)"
  RAWTOUCH=$(ssm_run <<EOF
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names <<'SQL'
SELECT t.table_schema, t.table_name
FROM information_schema.tables t
WHERE t.table_type = 'BASE TABLE'
  AND t.table_schema NOT IN ('mysql','information_schema','performance_schema','sys','innodb','tmp')
  AND t.table_schema NOT LIKE 'awsdms%'
  AND (t.update_time >= DATE_SUB('$SRC_EPOCH', INTERVAL 5 SECOND)
       OR EXISTS (SELECT 1 FROM information_schema.partitions p
                  WHERE p.table_schema = t.table_schema AND p.table_name = t.table_name
                    AND p.partition_name IS NOT NULL))
  AND EXISTS (SELECT 1 FROM information_schema.table_constraints tc
              WHERE tc.table_schema = t.table_schema AND tc.table_name = t.table_name
                AND tc.constraint_type IN ('PRIMARY KEY','UNIQUE'))
SQL
EOF
)
  TOUCHED=$(echo "$RAWTOUCH" | tr -d '\r' | grep . | jq -Rn '[inputs | split("\t") | select(length == 2) | {SchemaName: .[0], TableName: .[1]}]')
  NTOUCH=$(echo "$TOUCHED" | jq length)
  say "  tables touched in the fence window (revalidation set): $NTOUCH"
  echo "$TOUCHED" | jq -r '.[] | "    " + .SchemaName + "." + .TableName' | head -30
  [ "$NTOUCH" -ge 1 ] || die "revalidation set is empty - the marker table alone should be in it; the update_time derivation is broken, do not trust this epoch"
  echo "$TOUCHED" | jq -e '[.[] | select(.SchemaName == "aurora_mig_ctl" and .TableName == "marker")] | length == 1' >/dev/null || \
    die "the marker table is missing from the touched set - the update_time derivation is broken, do not trust this epoch"
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
  # rule-action "explicit" = exact-name match. "include" treats _ and % as
  # wildcards, so e.g. server_lock would also select serverXlock - an extra
  # matched table could then satisfy a count-based gate while a real touched
  # table was still pending. Belt and braces: the completion gate below also
  # compares the exact validated (schema, table) SET, never counts.
  VMAP=$(echo "$TOUCHED" | jq '{rules: [to_entries[] | {"rule-type":"selection","rule-id":(.key+1|tostring),"rule-name":(.key+1|tostring),"object-locator":{"schema-name":.value.SchemaName,"table-name":.value.TableName},"rule-action":"explicit"}]}')
  RIARN=$(aws dms describe-replication-instances --region "$REGION" --filters "Name=replication-instance-id,Values=${ID}-aurora-migration" --query 'ReplicationInstances[0].ReplicationInstanceArn' --output text)
  SEP=$(aws dms describe-endpoints --region "$REGION" --filters "Name=endpoint-id,Values=${ID}-aurora-migration-source" --query 'Endpoints[0].EndpointArn' --output text)
  TEP=$(aws dms describe-endpoints --region "$REGION" --filters "Name=endpoint-id,Values=${ID}-aurora-migration-target" --query 'Endpoints[0].EndpointArn' --output text)
  say "  creating the validation-only task ($VTASK_ID)"
  VARN=$(aws dms create-replication-task --region "$REGION" \
    --replication-task-identifier "$VTASK_ID" \
    --replication-instance-arn "$RIARN" \
    --source-endpoint-arn "$SEP" --target-endpoint-arn "$TEP" \
    --migration-type full-load \
    --table-mappings "$VMAP" \
    --replication-task-settings '{"FullLoadSettings":{"TargetTablePrepMode":"DO_NOTHING"},"ValidationSettings":{"EnableValidation":true,"ValidationOnly":true,"ThreadCount":5,"FailureMaxCount":10000},"Logging":{"EnableLogging":true}}' \
    --query 'ReplicationTask.ReplicationTaskArn' --output text)
  vwait_status ready 600
  aws dms start-replication-task --replication-task-arn "$VARN" --start-replication-task-type start-replication --region "$REGION" >/dev/null
  say "  validating the touched set (every touched table must reach Validated in this epoch)"
  VWAIT=0
  while :; do
    VSTATS=$(aws dms describe-table-statistics --replication-task-arn "$VARN" --region "$REGION" \
      --query 'TableStatistics[].{s:SchemaName,t:TableName,v:ValidationState}' --output json)
    NBAD=$(echo "$VSTATS" | jq '[.[] | select(.v | test("mismatch|error|suspend|no primary"; "i"))] | length')
    # exact SET comparison: every table in TOUCHED must appear in the fresh
    # task's stats as Validated. Counts alone could be satisfied by a stray
    # extra match while a real table is still pending.
    PENDING=$(jq -n --argjson want "$TOUCHED" --argjson now "$VSTATS" '
      ($now | map(select((.v | ascii_downcase) == "validated") | {key: (.s + "|" + .t), value: true}) | from_entries) as $ok
      | [ $want[] | select(($ok[.SchemaName + "|" + .TableName] // false) | not) ] | length')
    say "  pending=$PENDING/$NTOUCH (bad states: $NBAD)"
    [ "$NBAD" -gt 0 ] && die "fence-epoch validation found non-clean tables - inspect $VTASK_ID table statistics before touching anything"
    [ "$PENDING" = "0" ] && break
    VWAIT=$((VWAIT+20)); [ "$VWAIT" -gt 2700 ] && die "fence-epoch validation did not complete in 45m - inspect $VTASK_ID"
    sleep 20
  done
  say "  epoch validation PASSED - removing the validation task"
  vtask_teardown
  # main-task whole-task whitelist gate (its stats survive the stop): catches a
  # table that went Mismatched/Suspended during soak outside the touched set.
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
    aws dms start-replication-task --replication-task-arn "$(bi_task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
  fi
  say "ABORTED cleanly: source writable, Aurora untouched by the app, CDC re-established. Scale writers back up."
  ;;

bi-epoch)
  ST=$(bi_task_status)
  [ "$ST" = "stopped" ] || { [ "$ST" = "running" ] && { aws dms stop-replication-task --replication-task-arn "$(bi_task_arn)" --region "$REGION" >/dev/null; while [ "$(bi_task_status)" != "stopped" ]; do sleep 10; done; }; }
  say "reloading the BI target from the (now Aurora) source"
  aws dms start-replication-task --replication-task-arn "$(bi_task_arn)" --start-replication-task-type reload-target --region "$REGION" >/dev/null
  say "BI task reloading (its source endpoint followed the flipped db host at the cutover apply)"
  ;;

*) die "unknown phase: $PHASE" ;;
esac
