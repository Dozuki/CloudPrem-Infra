#!/usr/bin/env bash
# One-off teardown of the orphaned spacelift-leftover stacks in the DDVtest account.
# Targets the EXISTING unprefixed state keys standard/us-east-1/{usac,latam,min} in the
# DDVtest state bucket via the live/standard partition (account.hcl already = DDVtest,
# profile-direct auth, no ControlTowerExecution chain).
#
# NOT a harness test — run from your terminal (each destroy is 20-30 min; min has an
# EKS cluster). KEEPS dev-min and the harness foundation infra untouched.
#
# Usage:  ./teardown-orphans.sh            # all three
#         CONFIGS="usac latam" ./teardown-orphans.sh
set -euo pipefail
cd "$(dirname "$0")/../standard/us-east-1"

export AWS_PROFILE="${AWS_PROFILE:-default}"
# Resolved from the profile rather than carried as a literal, same as run.sh. The CONFIGS
# below are real state keys and stay, but the account id is just a variable default and
# there is no reason for it to sit in a public tree.
command -v aws >/dev/null || {
  echo "missing required tool: aws (needed to resolve the account id)" >&2; exit 1; }
TG_AWS_ACCT_ID="${TG_AWS_ACCT_ID:-$(aws sts get-caller-identity \
  --query Account --output text 2>/dev/null | tr -d '\r' || true)}"
case "$TG_AWS_ACCT_ID" in
  [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;;
  *)
    echo "ERROR: could not resolve the account id from AWS_PROFILE='$AWS_PROFILE'." >&2
    echo "       Refresh the profile, or export TG_AWS_ACCT_ID=<12-digit account>." >&2
    exit 1
    ;;
esac
export TG_AWS_ACCT_ID
export TG_AWS_PROFILE="$AWS_PROFILE"
export TG_AWS_REGION=us-east-1
export TG_STATE_PREFIX=""          # CRITICAL: hit the unprefixed orphaned state, not a harness run
export TG_TF_PATH=tofu

destroy() { # <env> <layer>
  local env=$1 layer=$2
  echo; echo "==================== destroy $env/$layer ===================="
  ( cd "$env/$layer" && terragrunt destroy --non-interactive -input=false )
}

CONFIGS="${CONFIGS:-usac latam min}"
for env in $CONFIGS; do
  case "$env" in
    usac|latam) destroy "$env" physical ;;                 # physical-only (16 resources: VPC+RDS)
    min)        destroy min physical ;;                       # logical SKIPPED: vault.internal + EKS API unreachable from workstation. Deleting the EKS cluster (in physical) removes the in-cluster helm/k8s resources; leftover logical AWS resources (secrets/KMS) + state cleaned separately.
    *)          echo "unknown env: $env" >&2; exit 1 ;;
  esac
done
echo; echo "ALL REQUESTED TEARDOWNS COMPLETE"
