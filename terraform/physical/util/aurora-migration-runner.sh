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
  aws ec2 describe-instances --region "$REGION" \
    --filters "Name=tag:Role,Values=Bastion" "Name=instance-state-name,Values=running" \
               "Name=tag:Environment,Values=${ID#*-}" \
    --query 'Reservations[0].Instances[0].InstanceId' --output text
}
rds_endpoint() { aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" --query 'DBInstances[0].Endpoint.Address' --output text; }
aurora_endpoint() { aws rds describe-db-clusters --db-cluster-identifier "$CLUSTER_ID" --region "$REGION" --query 'DBClusters[0].Endpoint' --output text; }
task_arn() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].ReplicationTaskArn' --output text; }
task_status() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].Status' --output text 2>/dev/null || echo none; }
bi_task_arn() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$ID" --query 'ReplicationTasks[0].ReplicationTaskArn' --output text 2>/dev/null; }
bi_task_status() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$ID" --query 'ReplicationTasks[0].Status' --output text 2>/dev/null || echo none; }
primary_secret() { aws secretsmanager list-secrets --region "$REGION" --query "SecretList[?starts_with(Name, '${ID}-database')].ARN | [0]" --output text; }
migration_secret() { aws secretsmanager list-secrets --region "$REGION" --query "SecretList[?starts_with(Name, '${ID}-aurora-migration')].ARN | [0]" --output text; }
src_pg() { aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" --query 'DBInstances[0].DBParameterGroups[0].DBParameterGroupName' --output text; }
primary_secret_host() { aws secretsmanager get-secret-value --region "$REGION" --secret-id "$(primary_secret)" --query SecretString --output text | jq -r .host; }

# DMS validation clean-state whitelist: anything else is unclean. Matched
# case-insensitively ("No primary key" casing differs across DMS versions).
unclean_validation_count() {
  aws dms describe-table-statistics --replication-task-arn "$(task_arn)" --region "$REGION" \
    --query 'TableStatistics[].ValidationState' --output text | tr '\t' '\n' | \
    awk 'BEGIN{IGNORECASE=1} !/^validated$/ && !/^no primary key$/ && NF {c++} END{print c+0}'
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
mariadb-dump --defaults-extra-file=/tmp/mig-src.cnf --no-data --no-create-info --no-create-db --routines --events --triggers --skip-opt --databases $(tr '\n' ' ' < schemas.txt) > objects.sql || true
echo "object artifact counts: routines=$($S -e "SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema NOT IN ('mysql','sys')") triggers=$($S -e "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema NOT IN ('mysql','sys')") events=$($S -e "SELECT COUNT(*) FROM information_schema.events WHERE event_schema NOT IN ('mysql','sys')")"
mariadb-dump --defaults-extra-file=/tmp/mig-src.cnf --no-data --skip-triggers --databases $(tr '\n' ' ' < schemas.txt) > full.sql
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
  aws dms start-replication-task-assessment --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null 2>&1 && say "premigration assessment started" || say "WARN: assessment API unavailable - preload inventory gates stand in"
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
  aws dms test-connection --replication-instance-arn "$RIARN" --endpoint-arn "$TEP" --region "$REGION" >/dev/null
  while [ "$(aws dms describe-connections --region "$REGION" --filters "Name=endpoint-arn,Values=$TEP" --query 'Connections[0].Status' --output text)" != "successful" ]; do sleep 10; done
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
A=\$(mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "CHECKSUM TABLE $t" | awk '{print \$2}')
B=\$(mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names -e "CHECKSUM TABLE $t" | awk '{print \$2}')
[ "\$A" = "\$B" ] && echo "MATCH \$A" || echo "MISMATCH src=\$A tgt=\$B"
EOF
)
      say "  $t: $(echo "$R" | tail -1)"; echo "$R" | grep -q MISMATCH && die "checksum mismatch on $t"
    done
  fi
  say "VALIDATION GATE PASS"
  ;;

fence)
  say "PRECONDITIONS (yours): app writers scaled to zero + DDL freeze in effect"
  prep_cnfs
  # BI task must stop BEFORE the cutover apply mutates its source endpoint
  # (DMS rejects endpoint modification on a running task).
  if [ "$(bi_task_status)" = "running" ]; then
    say "stopping the BI DMS task ahead of the endpoint repoint"
    aws dms stop-replication-task --replication-task-arn "$(bi_task_arn)" --region "$REGION" >/dev/null
    while [ "$(bi_task_status)" != "stopped" ]; do sleep 10; done
  fi
  MARK="cutover-$(date -u +%s)"
  say "killing every DB session except our own (by connection id - the app and
       this runner share the master user, so a username filter would spare the
       app), then committing marker '$MARK' in the same session"
  ssm_run <<EOF
set -e
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names <<'SQL'
CREATE DATABASE IF NOT EXISTS aurora_mig_ctl;
CREATE TABLE IF NOT EXISTS aurora_mig_ctl.marker (id INT AUTO_INCREMENT PRIMARY KEY, tag VARCHAR(64), at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6));
SQL
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "
  SELECT GROUP_CONCAT(CONCAT('CALL mysql.rds_kill(', id, ');') SEPARATOR ' ')
  FROM information_schema.processlist
  WHERE id != CONNECTION_ID() AND user NOT IN ('rdsadmin') AND command != 'Binlog Dump';" | \
  tr ' ' '\n' | grep -v '^\$' | while read -r stmt; do mariadb --defaults-extra-file=/tmp/mig-src.cnf -e "\$stmt" 2>/dev/null || true; done
mariadb --defaults-extra-file=/tmp/mig-src.cnf -e "INSERT INTO aurora_mig_ctl.marker (tag) VALUES ('$MARK');"
echo MARKER_COMMITTED
EOF
  say ""
  say ">>> NOW APPLY THE TERRAFORM FENCE: set aurora_migration_source_fenced = true"
  say ">>> in this env's env.hcl and confirm the gated physical apply, then return."
  say "Polling for read_only=1 (the apply + parameter propagation)..."
  while :; do RO=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT @@read_only"
EOF
); RO=$(echo "$RO" | tr -d '[:space:]'); say "  read_only=$RO"; [ "$RO" = "1" ] && break; sleep 30; done
  say "negative write test (must be refused)"
  ssm_run <<'EOF'
if mariadb --defaults-extra-file=/tmp/mig-src.cnf -e "INSERT INTO aurora_mig_ctl.marker (tag) VALUES ('must-fail');" 2>/tmp/neg.err; then echo FENCE_BREACH; exit 1; else echo FENCE_OK; fi
EOF
  say "final source binlog coordinate:"
  ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch -e "SHOW MASTER STATUS;" | head -2
EOF
  say "gate: marker on Aurora"
  while :; do N=$(ssm_run <<EOF
mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names -e "SELECT COUNT(*) FROM aurora_mig_ctl.marker WHERE tag='$MARK'" 2>/dev/null || echo 0
EOF
); N=$(echo "$N" | tr -d '[:space:]'); say "  marker_on_aurora=$N"; [ "$N" = "1" ] && break; sleep 10; done
  say "gate: validation fully clean (whitelist)"
  while :; do U=$(unclean_validation_count); say "  unclean=$U"; [ "$U" = "0" ] && break; sleep 20; done
  say "installing object artifacts on the target (routines/events/triggers; DMS"
  say "is about to stop for good, so triggers can never fire on replicated DML)"
  ssm_run <<'EOF'
set -e
if [ -s /tmp/mig/objects.sql ]; then
  mariadb --defaults-extra-file=/tmp/mig-tgt.cnf < /tmp/mig/objects.sql && echo OBJECTS_INSTALLED
else
  echo NO_OBJECTS_TO_INSTALL
fi
EOF
  say "final task stop + checkpoint"
  aws dms stop-replication-task --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null
  while [ "$(task_status)" != "stopped" ]; do sleep 10; done
  say "checkpoint: $(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].RecoveryCheckpoint' --output text)"
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
  # Hard guards: abort is PRE-CUTOVER ONLY. After the cutover apply the app is
  # writing to Aurora and reopening RDS would split-brain.
  H=$(primary_secret_host); A=$(aurora_endpoint 2>/dev/null || echo none)
  [ "$H" = "$A" ] && die "REFUSED: primary secret host already points at Aurora - cutover has been applied. Fail forward."
  prep_cnfs
  PW=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names -e "SELECT COUNT(*) FROM aurora_mig_ctl.marker WHERE tag LIKE 'post-promotion%'" 2>/dev/null || echo 0
EOF
); PW=$(echo "$PW" | tr -d '[:space:]')
  [ "$PW" = "0" ] || die "REFUSED: post-promotion writes exist on Aurora. Fail forward."
  say ">>> UNFENCE: set aurora_migration_source_fenced = false in env.hcl and"
  say ">>> confirm the gated apply. Polling for read_only=0..."
  while :; do RO=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT @@read_only"
EOF
); RO=$(echo "$RO" | tr -d '[:space:]'); say "  read_only=$RO"; [ "$RO" = "0" ] && break; sleep 30; done
  if [ "$(task_status)" = "stopped" ]; then
    say "re-establishing CDC from the checkpoint (so the migration can retry later)"
    aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
  fi
  if [ "$(bi_task_status)" = "stopped" ]; then
    say "resuming the BI task against the (still RDS) source"
    aws dms start-replication-task --replication-task-arn "$(bi_task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
  fi
  say "ABORTED cleanly: source writable again, Aurora untouched by the app, CDC re-established. Scale writers back up."
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
