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

# --- SSO session runway check ------------------------------------------------
# A run can outlive the AWS SSO session (an upgrade run is ~1h; the full matrix is
# several hours). If the session expires mid-apply, terraform loses its creds and
# leaves a half-built, hard-to-clean stack. Refuse to start unless the SSO session
# token has enough runway. Override with SKIP_SSO_CHECK=1; tune REQUIRED_SSO_HOURS.
REQUIRED_SSO_HOURS="${REQUIRED_SSO_HOURS:-3}"
sso_seconds_left() { # <profile> -> seconds left on the SSO session token (-1 if unknown)
  command -v python3 >/dev/null 2>&1 || { echo -1; return; }
  python3 - "$1" <<'PY'
import configparser, glob, hashlib, json, os, sys
from datetime import datetime, timezone
profile = sys.argv[1]
cfg = configparser.ConfigParser()
cfg.read(os.path.expanduser("~/.aws/config"))
sect = "default" if profile == "default" else "profile %s" % profile
session = start = None
if cfg.has_section(sect):
    session = cfg.get(sect, "sso_session", fallback=None)
    start = cfg.get(sect, "sso_start_url", fallback=None)
if session and not start and cfg.has_section("sso-session %s" % session):
    start = cfg.get("sso-session %s" % session, "sso_start_url", fallback=None)
targets = set()
for k in (session, start):
    if k:
        targets.add(os.path.expanduser("~/.aws/sso/cache/%s.json" % hashlib.sha1(k.encode()).hexdigest()))
def newest(files):
    best = None
    for f in files:
        try:
            d = json.load(open(f))
        except Exception:
            continue
        e = d.get("expiresAt")
        if e and d.get("accessToken"):
            best = e if best is None or e > best else best
    return best
allf = glob.glob(os.path.expanduser("~/.aws/sso/cache/*.json"))
best = newest([f for f in allf if f in targets]) or newest(allf)
if not best:
    print(-1); sys.exit(0)
try:
    dt = datetime.fromisoformat(best.replace("Z", "+00:00"))
except ValueError:
    print(-1); sys.exit(0)
print(int((dt - datetime.now(timezone.utc)).total_seconds()))
PY
}
sso_rem="$(sso_seconds_left "$AWS_PROFILE")"
if [ "$sso_rem" -ge 0 ] 2>/dev/null; then
  if [ "$sso_rem" -lt $((REQUIRED_SSO_HOURS * 3600)) ]; then
    echo "ERROR: AWS SSO session for '$AWS_PROFILE' has only $((sso_rem/3600))h$(((sso_rem%3600)/60))m left (< ${REQUIRED_SSO_HOURS}h)." >&2
    echo "       A run can outlive it and strand a half-built stack. Refresh first:" >&2
    echo "         aws sso login --profile $AWS_PROFILE" >&2
    echo "       (override with SKIP_SSO_CHECK=1, or lower REQUIRED_SSO_HOURS, for a short run.)" >&2
    [ "${SKIP_SSO_CHECK:-0}" = 1 ] || exit 1
  else
    echo ">> SSO: $((sso_rem/3600))h$(((sso_rem%3600)/60))m left on '$AWS_PROFILE' (>= ${REQUIRED_SSO_HOURS}h) — OK"
  fi
else
  echo ">> SSO: could not read session expiry for '$AWS_PROFILE'; ensure 'aws sso login --profile $AWS_PROFILE' is fresh." >&2
fi
# -----------------------------------------------------------------------------

VAULT_KUBE_CONTEXT="${VAULT_KUBE_CONTEXT:-vault-standard}"
VAULT_AWS_PROFILE="${VAULT_AWS_PROFILE:-dozuki}"
VAULT_AWS_ROLE="${VAULT_AWS_ROLE:-admin}"
VAULT_PF_PID=""

cleanup() {
  # Backstop teardown: once the run has started, always sweep THIS run's resources +
  # state on exit — scoped to $RUN_ID so it never touches another run's stack — even
  # if the harness's own deferred destroy didn't run or finish (e.g. the test was
  # interrupted/killed). It's a no-op once the run already cleaned itself, also purges
  # the leftover state objects the in-test destroy leaves, and disables the stack's
  # central-Vault auth mount. Runs BEFORE the tunnel is torn down so it can reuse this
  # session's VAULT_ADDR/VAULT_TOKEN for that Vault cleanup. Opt out: SKIP_AUTO_CLEANUP=1.
  if [ "${STARTED_RUN:-0}" = 1 ] && [ "${SKIP_AUTO_CLEANUP:-0}" != 1 ]; then
    echo ">> Auto-cleanup: sweeping this run's resources + state (${RUN_ID}) ..." >&2
    ./cleanup-orphans.sh "${RUN_ID}-" || echo ">> Auto-cleanup reported issues — see verify-clean output above." >&2
  fi
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

REQUIRED_BINS="git tofu terragrunt helm aws go openssl"
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

# Generate a throwaway self-signed cert and supply it as tls_cert/tls_key so the
# logical layer renders tls-secret directly (manual TLS), bypassing cert-manager/ACME
# — which can't issue reliably in an ephemeral test cluster (DNS-01 propagation, LE
# prod rate limits). Applies to both refs' logical (baseline v6.0.1+ and the upgrade);
# the physical layer ignores the unused TF_VARs. Override by presetting TF_VAR_tls_cert.
if [ -z "${TF_VAR_tls_cert:-}" ]; then
  _tlsdir="$(mktemp -d)"
  openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
    -keyout "$_tlsdir/tls.key" -out "$_tlsdir/tls.crt" \
    -subj "/O=Dozuki smoke test/CN=dozuki.cloud" \
    -addext "subjectAltName=DNS:dozuki.cloud,DNS:*.dozuki.cloud" >/dev/null 2>&1
  export TF_VAR_tls_cert="$(base64 < "$_tlsdir/tls.crt" | tr -d '\n')"
  export TF_VAR_tls_key="$(base64 < "$_tlsdir/tls.key" | tr -d '\n')"
  echo ">> TLS: generated self-signed cert -> manual TLS (no cert-manager/ACME)."
fi

# From here on, the run can create cloud resources — arm the backstop cleanup (see trap).
STARTED_RUN=1
go test ./scenarios/ -run TestUpgrade -v -timeout 180m
