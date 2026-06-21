#!/usr/bin/env bash
# One-off teardown of the orphaned spacelift-leftover stacks in DDVtest (076248559428).
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

export TG_AWS_ACCT_ID=076248559428
export TG_AWS_PROFILE=default
export TG_AWS_REGION=us-east-1
export TG_STATE_PREFIX=""          # CRITICAL: hit the unprefixed orphaned state, not a harness run
export TERRAGRUNT_TFPATH=tofu
export AWS_PROFILE=default

destroy() { # <env> <layer>
  local env=$1 layer=$2
  echo; echo "==================== destroy $env/$layer ===================="
  ( cd "$env/$layer" && terragrunt destroy --terragrunt-non-interactive -input=false )
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
