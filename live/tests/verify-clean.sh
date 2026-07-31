#!/usr/bin/env bash
# verify-clean.sh — leak detector for the upgrade harness.
#
# Scans the cost-heavy + collision-prone AWS services for resources belonging to
# a harness identity (by name prefix, default "smoke-") and the state bucket for
# orphaned per-run state prefixes. READ-ONLY: it reports and exits non-zero if
# anything is found — it never deletes. Use it as a post-run check or a gate
# before re-running, so an orphan is never silent.
#
# Usage:  ./verify-clean.sh [name-prefix]      # default prefix: smoke
#   AWS_PROFILE=default ./verify-clean.sh
#   AWS_PROFILE=default ./verify-clean.sh dozuki-min   # check a specific stack id
#
# (This is the quick name-based detector. The durable version — tag-based via the
#  Resource Groups Tagging API + auto-sweep — is folded into the janitor project.)
set -uo pipefail

PREFIX="${1:-smoke}"
P="${AWS_PROFILE:-default}"
PRIMARY="${AWS_REGION:-us-east-1}"
DR="${DR_REGION:-us-west-2}"
ACCT="$(aws sts get-caller-identity --profile "$P" --query Account --output text 2>/dev/null)"
# Both buckets: the recover scenario deploys its rebuild stack INTO the DR region, so
# that stack's state lives in the DR region's bucket. Checking only the primary let five
# DR-region prefixes accumulate unnoticed across cycles while this reported CLEAN.
STATE_BUCKET="dozuki-terraform-state-${PRIMARY}-${ACCT}"
DR_STATE_BUCKET="dozuki-terraform-state-${DR}-${ACCT}"

[ -z "$ACCT" ] && { echo "ERROR: no AWS identity (profile=$P)" >&2; exit 2; }

leaks=0
report() { # <label> <space/newline-separated values>
  local label="$1" val
  val="$(echo "${2:-}" | tr '\t' '\n' | sed '/^None$/d;/^$/d')"
  if [ -n "$val" ]; then
    while IFS= read -r v; do echo "  LEAK [$label] $v"; leaks=$((leaks+1)); done <<<"$val"
  fi
}

echo "=== verify-clean: account $ACCT, prefix '${PREFIX}-' ==="

for R in "$PRIMARY" "$DR"; do
  echo "--- region $R ---"
  report "eks/$R"             "$(aws eks list-clusters --region "$R" --profile "$P" --query "clusters[?starts_with(@,'${PREFIX}-')]" --output text 2>/dev/null)"
  report "rds/$R"             "$(aws rds describe-db-instances --region "$R" --profile "$P" --query "DBInstances[?starts_with(DBInstanceIdentifier,'${PREFIX}-')].DBInstanceIdentifier" --output text 2>/dev/null)"
  report "rds-bkup/$R"        "$(aws rds describe-db-instance-automated-backups --region "$R" --profile "$P" --query "DBInstanceAutomatedBackups[?starts_with(DBInstanceIdentifier,'${PREFIX}-')].DBInstanceIdentifier" --output text 2>/dev/null)"
  report "vpc/$R"             "$(aws ec2 describe-vpcs --region "$R" --profile "$P" --query "Vpcs[?Tags[?Key=='Name'&&starts_with(Value,'${PREFIX}-')]].VpcId" --output text 2>/dev/null)"
  report "elbv2/$R"           "$(aws elbv2 describe-load-balancers --region "$R" --profile "$P" --query "LoadBalancers[?starts_with(LoadBalancerName,'${PREFIX}-')].LoadBalancerName" --output text 2>/dev/null)"
  report "elasticache/$R"     "$(aws elasticache describe-cache-clusters --region "$R" --profile "$P" --query "CacheClusters[?starts_with(CacheClusterId,'${PREFIX}-')].CacheClusterId" --output text 2>/dev/null)"
  report "elasticache-pg/$R"  "$(aws elasticache describe-cache-parameter-groups --region "$R" --profile "$P" --query "CacheParameterGroups[?starts_with(CacheParameterGroupName,'${PREFIX}-')].CacheParameterGroupName" --output text 2>/dev/null)"
  report "elasticache-sng/$R" "$(aws elasticache describe-cache-subnet-groups --region "$R" --profile "$P" --query "CacheSubnetGroups[?starts_with(CacheSubnetGroupName,'${PREFIX}-')].CacheSubnetGroupName" --output text 2>/dev/null)"
  report "db-subnet-grp/$R"   "$(aws rds describe-db-subnet-groups --region "$R" --profile "$P" --query "DBSubnetGroups[?starts_with(DBSubnetGroupName,'${PREFIX}-')].DBSubnetGroupName" --output text 2>/dev/null)"
  report "db-param-grp/$R"    "$(aws rds describe-db-parameter-groups --region "$R" --profile "$P" --query "DBParameterGroups[?starts_with(DBParameterGroupName,'${PREFIX}-')].DBParameterGroupName" --output text 2>/dev/null)"
  report "ssm-docs/$R"        "$(aws ssm list-documents --region "$R" --profile "$P" --filters Key=Owner,Values=Self --query "DocumentIdentifiers[?contains(Name,'-${PREFIX}-')||starts_with(Name,'${PREFIX}-')].Name" --output text 2>/dev/null)"
  report "sqs/$R"             "$(aws sqs list-queues --region "$R" --profile "$P" --queue-name-prefix "${PREFIX}-" --query 'QueueUrls[]' --output text 2>/dev/null)"
  report "sns/$R"             "$(aws sns list-topics --region "$R" --profile "$P" --query "Topics[?contains(TopicArn,':${PREFIX}-')].TopicArn" --output text 2>/dev/null)"
  report "loggroups/$R"       "$(aws logs describe-log-groups --region "$R" --profile "$P" --query "logGroups[?contains(logGroupName,'${PREFIX}-')].logGroupName" --output text 2>/dev/null)"
  report "kms-aliases/$R"     "$(aws kms list-aliases --region "$R" --profile "$P" --query "Aliases[?contains(AliasName,'${PREFIX}-')].AliasName" --output text 2>/dev/null)"
  report "secrets/$R"         "$(aws secretsmanager list-secrets --region "$R" --profile "$P" --include-planned-deletion --query "SecretList[?starts_with(Name,'${PREFIX}-')].Name" --output text 2>/dev/null)"
  report "secgroups/$R"       "$(aws ec2 describe-security-groups --region "$R" --profile "$P" --query "SecurityGroups[?Tags[?Key=='Name'&&starts_with(Value,'${PREFIX}-')]].GroupId" --output text 2>/dev/null)"
done

echo "--- global ---"
report "iam-roles"    "$(aws iam list-roles --profile "$P" --query "Roles[?starts_with(RoleName,'${PREFIX}-')].RoleName" --output text 2>/dev/null)"
report "iam-policies" "$(aws iam list-policies --scope Local --profile "$P" --query "Policies[?starts_with(PolicyName,'${PREFIX}-')].PolicyName" --output text 2>/dev/null)"
report "s3"           "$(aws s3api list-buckets --profile "$P" --query "Buckets[?starts_with(Name,'${PREFIX}-')].Name" --output text 2>/dev/null)"

echo "--- vault ---"
# Vault leftovers are invisible to every AWS sweep above and the reaper never touches
# them; an orphaned k8s auth mount 400s the next logical apply of the same stack id
# (found live 2026-07-31: k8s/smoke-min outlived its run and blocked the rerun).
# Needs VAULT_ADDR + a usable token; degrades to a WARN, never a silent pass.
if command -v vault >/dev/null && [ -n "${VAULT_ADDR:-}" ] && vault token lookup >/dev/null 2>&1; then
  report "vault-policy" "$(vault policy list 2>/dev/null | grep "^${PREFIX}-\|^${PREFIX}\$")"
  _mounts="$(vault auth list -format=json 2>/dev/null | jq -r 'keys[]' 2>/dev/null)"
  if [ -n "$_mounts" ]; then
    report "vault-auth" "$(printf '%s\n' "$_mounts" | grep "^k8s/${PREFIX}")"
  else
    # sys/auth list needs sudo, which the deployer/admin tokens may lack. The logical
    # layer creates the mount and the policy together, so probe the mounts implied by
    # the policy names instead of passing silently.
    for _pol in $(vault policy list 2>/dev/null | grep "^${PREFIX}-\|^${PREFIX}\$" | grep -v -- '-eso$'); do
      vault read "sys/auth/k8s/${_pol}/tune" >/dev/null 2>&1 && report "vault-auth(probe)" "k8s/${_pol}"
    done
    echo "  WARN vault: sys/auth not listable with this token; mounts probed via policy names only"
  fi
else
  echo "  WARN vault: no VAULT_ADDR or no valid token - vault leftovers NOT checked"
fi

echo "--- terraform state ---"
# Orphaned per-run harness state prefixes (RunUpgrade uses local-<ts>-<cfg>/...).
_state_prefixes() { # <bucket> — prints "prefix (bucket-region)" so a leak names where it lives
  local b="$1"
  aws s3 ls "s3://$b/" --profile "$P" 2>/dev/null | awk -v b="$b" '/PRE local-/{gsub(/\//,"",$2); print $2 " (" b ")"}'
}
_all_state="$(_state_prefixes "$STATE_BUCKET")"
if [ "$DR_STATE_BUCKET" != "$STATE_BUCKET" ]; then
  _all_state="$_all_state
$(_state_prefixes "$DR_STATE_BUCKET")"
fi
report "state-prefix" "$(printf '%s\n' "$_all_state" | awk 'NF')"

echo "======================================================"
if [ "$leaks" -eq 0 ]; then
  echo "CLEAN — no '${PREFIX}-' resources or orphaned run-state found."
  exit 0
fi
echo "LEAKS FOUND: $leaks item(s) above need cleanup before re-running."
exit 1
