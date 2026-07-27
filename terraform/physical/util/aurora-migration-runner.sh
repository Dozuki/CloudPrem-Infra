#!/usr/bin/env bash
# RDS -> Aurora Serverless v2 migration runner. Drives the DB-side phases the
# aurora_migration_state Terraform (aurora-migration.tf) cannot: native schema
# pre-load, DMS task orchestration, validation gates, and the fence/cutover
# sequence. Every step is the 2026-07-26 gca-rehearsal procedure, productionized
# (design doc: 3m-aurora-dms-migration-design).
#
# Runs on an operator machine with aws cli + jq; all SQL executes ON THE ENV
# BASTION via SSM (the bastion has mariadb + the primary-DB .my.cnf from its
# State Manager association). Nothing here talks to a database directly.
#
# Usage:
#   AWS_PROFILE=... aurora-migration-runner.sh <identifier> <region> <phase>
# Phases (in order):
#   status    - show everything (safe anytime)
#   preload   - schema dump/split, dangling-FK scan, base DDL, md5 diff gate
#   load      - premigration assessment, start task, wait auto-stop, deferred
#               DDL (indexes then FKs = orphan check), drop the FK Initstmt,
#               resume CDC
#   validate  - DMS validation clean + checksums (frozen check; run pre-fence)
#   fence     - the cutover fence: writer scale-down is YOURS (kubectl); this
#               does marker + read_only + negative test + the go-live gate and
#               stops the task at its checkpoint. Prints GO/NO-GO.
#   cutover-verify - post-`aurora_migration_state=cutover` apply: identity +
#               committed-write check on Aurora, event scheduler assert.
#   abort     - pre-first-write abort: revert read_only, resume writers on RDS.
#   bi-epoch  - restart the BI DMS task against the (now Aurora) source.
set -euo pipefail

ID="${1:?identifier (e.g. m3-gca)}"; REGION="${2:?region}"; PHASE="${3:?phase}"
RUN_TAG="aurora-mig-$(date -u +%Y%m%d)"
SRC_ID="$ID"                       # module.primary_database db identifier
CLUSTER_ID="$ID"                   # module.aurora cluster identifier
TASK_ID="${ID}-aurora-migration"   # aws_dms_replication_task.aurora_migration

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
task_status() { aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].Status' --output text; }
secret_arn() { aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" >/dev/null && aws secretsmanager list-secrets --region "$REGION" --query "SecretList[?starts_with(Name, '${ID}-database')].ARN | [0]" --output text; }
src_pg() { aws rds describe-db-instances --db-instance-identifier "$SRC_ID" --region "$REGION" --query 'DBInstances[0].DBParameterGroups[0].DBParameterGroupName' --output text; }

# --- bastion exec (SSM) -------------------------------------------------------
# ssm_run <<'EOF' ... EOF  -> runs the heredoc as one shell script on the bastion,
# echoes its stdout, dies on failure. jq builds the params JSON (quote-safe).
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

# Write /tmp/mig-{src,tgt}.cnf on the bastion from the credentials secret (same
# username/password on both sides; only the host differs). Explicit hosts - the
# association's .my.cnf follows local.db_host, which flips at cutover.
prep_cnfs() {
  local sec src tgt; sec=$(secret_arn); src=$(rds_endpoint); tgt=$(aurora_endpoint)
  ssm_run <<EOF
set -e; umask 177
C=\$(aws secretsmanager get-secret-value --region $REGION --secret-id "$sec" --query SecretString --output text)
U=\$(echo "\$C" | jq -r .username); P=\$(echo "\$C" | jq -r .password)
for pair in "src $src" "tgt $tgt"; do set -- \$pair
  printf '[client]\nhost=%s\nuser=%s\npassword=%s\nssl\n' "\$2" "\$U" "\$P" > /tmp/mig-\$1.cnf
done
echo CNFS_READY
EOF
}
msrc() { echo "mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names"; }
mtgt() { echo "mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names --init-command=\"SET SESSION restrict_fk_on_non_standard_key=0\""; }

# =============================================================================
case "$PHASE" in

status)
  say "task: $(task_status 2>/dev/null || echo none)  rds: $(rds_endpoint 2>/dev/null || echo none)  aurora: $(aurora_endpoint 2>/dev/null || echo none)"
  ;;

preload)
  prep_cnfs
  ssm_run <<'EOF'
set -e; cd /tmp; rm -rf mig && mkdir mig && cd mig
S="mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names"
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names --init-command='SET SESSION restrict_fk_on_non_standard_key=0'"
# gate: source CDC prereqs
$S -e "SELECT @@binlog_format, @@binlog_row_image, @@log_bin" | grep -q "ROW	FULL	1" || { echo "SOURCE NOT CDC-READY"; exit 1; }
# gate: lower_case_table_names must match (immutable on the target)
[ "$($S -e 'SELECT @@lower_case_table_names')" = "$(eval $T -e "'SELECT @@lower_case_table_names'")" ] || { echo LCTN_MISMATCH; exit 1; }
# schema list (system + DMS control schemas excluded)
$S -e "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('mysql','information_schema','performance_schema','sys','innodb','tmp','awsdms_control')" > schemas.txt
# dangling-FK scan: FKs whose referenced table does not exist are unenforceable
# residue (created with FK checks off) - excluded from the target, reported.
$S -e "SELECT CONCAT(k.constraint_schema,'.',k.table_name,'.',k.constraint_name) FROM information_schema.key_column_usage k LEFT JOIN information_schema.tables t ON t.table_schema=k.referenced_table_schema AND t.table_name=k.referenced_table_name WHERE k.referenced_table_name IS NOT NULL AND t.table_name IS NULL" > dangling-fks.txt
echo "dangling FKs: $(wc -l < dangling-fks.txt)"; cat dangling-fks.txt
# dump DDL, split base (tables w/o secondary indexes+FKs) vs deferred
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
# md5 diff gate: identical column definitions on both sides (base tables only)
Q="SELECT c.table_schema,c.table_name,c.column_name,c.column_type,c.is_nullable,IFNULL(c.column_default,'~N~'),c.extra FROM information_schema.columns c JOIN information_schema.tables t ON c.table_schema=t.table_schema AND c.table_name=t.table_name WHERE t.table_type='BASE TABLE' AND c.table_schema NOT IN ('mysql','information_schema','performance_schema','sys','innodb','tmp','awsdms_control') ORDER BY c.table_schema,c.table_name,c.ordinal_position"
$S -e "$Q" > cols-src.tsv; eval $T -e "\"$Q\"" > cols-tgt.tsv
[ "$(md5sum < cols-src.tsv)" = "$(md5sum < cols-tgt.tsv)" ] && echo MD5_GATE_PASS || { echo MD5_GATE_FAIL; diff cols-src.tsv cols-tgt.tsv | head -20; exit 1; }
EOF
  say "preload complete - md5 gate passed; review any dangling FKs above"
  ;;

load)
  [ "$(task_status)" = "ready" ] || die "task not ready (status=$(task_status))"
  say "premigration assessment"
  aws dms start-replication-task-assessment --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null || say "WARN: assessment API unavailable - inventory gates from preload stand in"
  say "starting full load + CDC"
  aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type start-replication --region "$REGION" >/dev/null
  say "waiting for the automatic post-full-load stop (StopTaskCachedChangesApplied)"
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
  say "waiting for the endpoint re-test (modify invalidates it)"
  while [ "$(aws dms describe-connections --region "$REGION" --filters "Name=endpoint-arn,Values=$TEP" --query 'Connections[0].Status' --output text)" != "successful" ]; do sleep 10; done
  say "resuming CDC (fresh sessions, FK checks ON)"
  aws dms start-replication-task --replication-task-arn "$(task_arn)" --start-replication-task-type resume-processing --region "$REGION" >/dev/null
  while [ "$(task_status)" != "running" ]; do sleep 10; done
  say "load complete - CDC running. Soak >= 1 day, then 'validate'."
  ;;

validate)
  say "DMS validation states:"
  aws dms describe-table-statistics --replication-task-arn "$(task_arn)" --region "$REGION" --query 'TableStatistics[].ValidationState' --output text | tr '\t' '\n' | sort | uniq -c
  BAD=$(aws dms describe-table-statistics --replication-task-arn "$(task_arn)" --region "$REGION" --query 'length(TableStatistics[?ValidationState!=`Validated` && ValidationState!=`No primary Key`])' --output text)
  [ "$BAD" = "0" ] && say "VALIDATION GATE PASS (no-PK tables are the only exceptions - review the list above)" || die "validation gate FAIL: $BAD tables not Validated"
  ;;

fence)
  say "PRECONDITION (yours): app writers scaled down (queueworkerd/crond/app write tier) + DDL freeze in effect"
  prep_cnfs
  SEC=$(secret_arn); MARK="cutover-$(date -u +%s)"
  say "step 1: kill remaining app sessions + commit marker '$MARK'"
  ssm_run <<EOF
set -e
S="mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names"
\$S -e "CREATE DATABASE IF NOT EXISTS aurora_mig_ctl; CREATE TABLE IF NOT EXISTS aurora_mig_ctl.marker (id INT AUTO_INCREMENT PRIMARY KEY, tag VARCHAR(64), at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6));"
for id in \$(\$S -e "SELECT id FROM information_schema.processlist WHERE user NOT IN ('rdsadmin','\$(\$S -e 'SELECT CURRENT_USER()' | cut -d@ -f1)') AND command != 'Binlog Dump'"); do \$S -e "CALL mysql.rds_kill(\$id);" || true; done
\$S -e "INSERT INTO aurora_mig_ctl.marker (tag) VALUES ('$MARK');"
echo MARKER_COMMITTED
EOF
  say "step 2: read_only=1 via parameter group + poll"
  aws rds modify-db-parameter-group --db-parameter-group-name "$(src_pg)" --region "$REGION" --parameters "ParameterName=read_only,ParameterValue=1,ApplyMethod=immediate" >/dev/null
  while :; do RO=$(ssm_run <<'EOF'
mariadb --defaults-extra-file=/tmp/mig-src.cnf --batch --skip-column-names -e "SELECT @@read_only"
EOF
); RO=$(echo "$RO" | tr -d '[:space:]'); say "  read_only=$RO"; [ "$RO" = "1" ] && break; sleep 15; done
  say "step 3: negative write test (must be refused)"
  ssm_run <<'EOF'
if mariadb --defaults-extra-file=/tmp/mig-src.cnf -e "INSERT INTO aurora_mig_ctl.marker (tag) VALUES ('must-fail');" 2>/tmp/neg.err; then echo FENCE_BREACH; exit 1; else echo FENCE_OK; fi
EOF
  say "step 4: go-live gate - marker on Aurora + validation drained + task stop"
  while :; do N=$(ssm_run <<EOF
mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names -e "SELECT COUNT(*) FROM aurora_mig_ctl.marker WHERE tag='$MARK'" 2>/dev/null || echo 0
EOF
); N=$(echo "$N" | tr -d '[:space:]'); say "  marker_on_aurora=$N"; [ "$N" = "1" ] && break; sleep 10; done
  while :; do P=$(aws dms describe-table-statistics --replication-task-arn "$(task_arn)" --region "$REGION" --query 'length(TableStatistics[?ValidationState==`Pending records`||ValidationState==`Suspended records`||ValidationState==`Mismatched records`])' --output text); say "  unclean_validation_tables=$P"; [ "$P" = "0" ] && break; sleep 20; done
  aws dms stop-replication-task --replication-task-arn "$(task_arn)" --region "$REGION" >/dev/null
  while [ "$(task_status)" != "stopped" ]; do sleep 10; done
  say "GO: gate passed, task stopped at checkpoint: $(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$TASK_ID" --query 'ReplicationTasks[0].RecoveryCheckpoint' --output text)"
  say "NEXT: apply aurora_migration_state=cutover (Spacelift), then run 'cutover-verify'"
  ;;

cutover-verify)
  prep_cnfs
  ssm_run <<'EOF'
set -e
T="mariadb --defaults-extra-file=/tmp/mig-tgt.cnf --batch --skip-column-names"
$T -e "SELECT @@aurora_version" | grep -q "8.4" || { echo WRONG_TARGET; exit 1; }
$T -e "INSERT INTO aurora_mig_ctl.marker (tag) VALUES ('PROMOTION-FIRST-WRITE');" && echo AURORA_WRITE_COMMITTED
echo "event_scheduler=$($T -e 'SELECT @@event_scheduler')"
EOF
  say "point of no return crossed - fail forward from here. Scale writers back up; leave RDS read_only."
  ;;

abort)
  say "pre-first-write abort: unfencing the source"
  aws rds modify-db-parameter-group --db-parameter-group-name "$(src_pg)" --region "$REGION" --parameters "ParameterName=read_only,ParameterValue=0,ApplyMethod=immediate" >/dev/null
  say "read_only reverting - verify with 'status', scale writers back up on RDS. Aurora untouched."
  ;;

bi-epoch)
  say "restarting the BI DMS task with a full reload against the (now Aurora) source"
  BI=$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$ID" --query 'ReplicationTasks[0].{arn:ReplicationTaskArn,st:Status}' --output json)
  ARN=$(echo "$BI" | jq -r .arn); ST=$(echo "$BI" | jq -r .st)
  [ "$ST" = "running" ] && { aws dms stop-replication-task --replication-task-arn "$ARN" --region "$REGION" >/dev/null; while [ "$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-id,Values=$ID" --query 'ReplicationTasks[0].Status' --output text)" != "stopped" ]; do sleep 10; done; }
  aws dms start-replication-task --replication-task-arn "$ARN" --start-replication-task-type reload-target --region "$REGION" >/dev/null
  say "BI task reloading from Aurora (its source endpoint already follows the flipped db host)"
  ;;

*) die "unknown phase: $PHASE" ;;
esac
