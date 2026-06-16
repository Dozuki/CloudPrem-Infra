#!/usr/bin/env bash
# Local entrypoint for the upgrade harness. Example:
#   AWS_PROFILE=ddvtest DDVTEST_ACCOUNT_ID=07XXXXXXXXXX \
#   FROM_REF=v6.0 TO_REF=v6.1-release CONFIGS=min_default ./run.sh
set -euo pipefail
cd "$(dirname "$0")"

: "${DDVTEST_ACCOUNT_ID:?set DDVTEST_ACCOUNT_ID}"
export RUN_INTEGRATION=1
export RUN_ID="${RUN_ID:-local-$(date +%s)}"
export AWS_PROFILE="${AWS_PROFILE:-ddvtest}"

for bin in git terraform terragrunt helm aws go; do
  command -v "$bin" >/dev/null || { echo "missing required tool: $bin" >&2; exit 1; }
done

go test ./scenarios/ -run TestUpgrade -v -timeout 180m
