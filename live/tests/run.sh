#!/usr/bin/env bash
# Local entrypoint for the upgrade harness. Example:
#   AWS_PROFILE=default DDVTEST_ACCOUNT_ID=07XXXXXXXXXX \
#   FROM_REF=v6.0 TO_REF=v6.1-release CONFIGS=min_default ./run.sh
#
# Vault: the logical layer's vault provider seeds per-stack secrets in the central
# Vault (in the dozuki/0106 account). Locally that's reached via a kubectl
# port-forward + AWS-auth login. This script brings the tunnel up, logs in, and
# tears the tunnel down on exit — fully hands-off off your AWS SSO session.
#
# Skip the tunnel (CI, or you've already exported VAULT_ADDR/VAULT_TOKEN) with:
#   SKIP_VAULT_TUNNEL=1
# Override the vault access defaults with VAULT_KUBE_CONTEXT / VAULT_AWS_PROFILE /
# VAULT_AWS_ROLE if needed.
set -euo pipefail
cd "$(dirname "$0")"

: "${DDVTEST_ACCOUNT_ID:?set DDVTEST_ACCOUNT_ID}"
export RUN_INTEGRATION=1
export RUN_ID="${RUN_ID:-local-$(date +%s)}"

# Tee everything to a logfile — the harness output is huge and terminal scrollback
# is painful to search after a failure. Override the path with RUN_LOG.
RUN_LOG="${RUN_LOG:-$PWD/.logs/${RUN_ID}.log}"
mkdir -p "$(dirname "$RUN_LOG")"
exec > >(tee -a "$RUN_LOG") 2>&1
echo ">> Logging all output to: $RUN_LOG"

# AWS profile for the DDVtest account (the default profile maps to it).
# Drop any inherited static AWS creds (e.g. from a prior `aws configure
# export-credentials` eval in your shell). While set they OVERRIDE AWS_PROFILE for
# every AWS call, so once they expire they break both the DDVtest Terraform run and
# the dozuki vault/kube auth even when your SSO sessions are valid. Force profile auth.
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
export AWS_PROFILE="${AWS_PROFILE:-default}"
# The physical layer requires OpenTofu (master_password_wo); drive terragrunt with tofu.
export TERRAGRUNT_TFPATH="${TERRAGRUNT_TFPATH:-tofu}"

VAULT_KUBE_CONTEXT="${VAULT_KUBE_CONTEXT:-vault-standard}"
VAULT_AWS_PROFILE="${VAULT_AWS_PROFILE:-dozuki}"
VAULT_AWS_ROLE="${VAULT_AWS_ROLE:-admin}"
VAULT_PF_PID=""

cleanup() {
  [ -n "$VAULT_PF_PID" ] && kill "$VAULT_PF_PID" 2>/dev/null || true
  echo ">> Full run log saved to: $RUN_LOG" >&2
}
trap cleanup EXIT

setup_vault() {
  # The vault kube context AND the vault AWS-auth login both authenticate as
  # VAULT_AWS_PROFILE (dozuki / 0106 — the account the Vault cluster lives in).
  # If that SSO session is expired, the port-forward's get-token fails and the
  # tunnel silently never comes up. Check up front with an actionable message.
  if ! aws sts get-caller-identity --profile "$VAULT_AWS_PROFILE" >/dev/null 2>&1; then
    echo "ERROR: AWS profile '$VAULT_AWS_PROFILE' (used by the $VAULT_KUBE_CONTEXT kube context + vault login) has no valid session." >&2
    echo "       Run:  aws sso login --profile $VAULT_AWS_PROFILE" >&2
    exit 1
  fi

  local pflog; pflog="$(mktemp -t vault-pf.XXXXXX)"
  echo ">> Vault: port-forward ${VAULT_KUBE_CONTEXT} -n vault svc/vault-active 8200 ..."
  kubectl --context "$VAULT_KUBE_CONTEXT" port-forward -n vault svc/vault-active 8200:8200 >"$pflog" 2>&1 &
  VAULT_PF_PID=$!

  # Wait for the tunnel and detect http vs https.
  for i in $(seq 1 30); do
    if curl -s -o /dev/null --max-time 2 http://127.0.0.1:8200/v1/sys/seal-status; then
      export VAULT_ADDR="http://127.0.0.1:8200"; break
    elif curl -sk -o /dev/null --max-time 2 https://127.0.0.1:8200/v1/sys/seal-status; then
      export VAULT_ADDR="https://127.0.0.1:8200"; export VAULT_SKIP_VERIFY=true; break
    fi
    [ "$i" = 30 ] && { echo "ERROR: vault port-forward never came up on :8200" >&2; echo "--- kubectl port-forward output: ---" >&2; cat "$pflog" >&2; exit 1; }
    sleep 1
  done
  echo ">> Vault: reachable at $VAULT_ADDR"

  # AWS-auth login in a SUBSHELL so the dozuki SSO creds (needed by the vault CLI's
  # aws auth, which can't read the SSO cache directly) never leak into the main env
  # — the Terraform run must stay on AWS_PROFILE=$AWS_PROFILE for the DDVtest account.
  echo ">> Vault: aws login (profile=$VAULT_AWS_PROFILE role=$VAULT_AWS_ROLE) ..."
  VAULT_TOKEN="$(
    eval "$(aws --profile "$VAULT_AWS_PROFILE" configure export-credentials --format env)"
    vault login -method=aws role="$VAULT_AWS_ROLE" -format=json \
      | python3 -c 'import sys,json; print(json.load(sys.stdin)["auth"]["client_token"])'
  )" || { echo "ERROR: vault aws login failed" >&2; exit 1; }
  [ -n "$VAULT_TOKEN" ] || { echo "ERROR: empty vault token" >&2; exit 1; }
  export VAULT_TOKEN
  echo ">> Vault: token acquired."
}

REQUIRED_BINS="git tofu terragrunt helm aws go"
DO_VAULT=1
if [ -n "${VAULT_TOKEN:-}" ] || [ "${SKIP_VAULT_TUNNEL:-0}" = 1 ]; then
  DO_VAULT=0
  echo ">> Vault: tunnel/auth skipped (VAULT_TOKEN preset or SKIP_VAULT_TUNNEL=1)."
else
  REQUIRED_BINS="$REQUIRED_BINS kubectl vault curl python3"
fi

for bin in $REQUIRED_BINS; do
  command -v "$bin" >/dev/null || { echo "missing required tool: $bin" >&2; exit 1; }
done

[ "$DO_VAULT" = 1 ] && setup_vault

go test ./scenarios/ -run TestUpgrade -v -timeout 180m
