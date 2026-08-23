#!/usr/bin/env bash
# Local entrypoint for the upgrade harness. Example:
#   AWS_PROFILE=default DDVTEST_ACCOUNT_ID=07XXXXXXXXXX \
#   FROM_REF=v6.0 TO_REF=v6.1-release CONFIGS=min_default ./run.sh
#
# Vault: the logical layer's vault provider seeds per-stack secrets in the central
# Vault (in the dozuki management account). Locally that's reached via a kubectl
# port-forward + AWS-auth login. This script brings the tunnel up, logs in, and
# tears the tunnel down on exit — fully hands-off off your AWS SSO session.
#
# Skip the tunnel (CI, or you've already exported VAULT_ADDR/VAULT_TOKEN) with:
#   SKIP_VAULT_TUNNEL=1
# Override the vault access defaults with VAULT_KUBE_CONTEXT / VAULT_AWS_PROFILE /
# VAULT_AWS_ROLE if needed.
set -euo pipefail
cd "$(dirname "$0")"

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
# TG_TF_PATH is the post-0.78 name; TERRAGRUNT_TFPATH still works but warns on every
# command and is slated for removal. Both accepted as input so an existing shell that
# exports the old one keeps working.
export TG_TF_PATH="${TG_TF_PATH:-${TERRAGRUNT_TFPATH:-tofu}}"
unset TERRAGRUNT_TFPATH

# --- SSO session runway check ------------------------------------------------
# A run can outlive the AWS SSO session (an upgrade run is ~1h; the full matrix is
# several hours). If the session expires mid-apply, terraform loses its creds and
# leaves a half-built, hard-to-clean stack. Refuse to start unless the SSO session
# token has enough runway. Override with SKIP_SSO_CHECK=1; tune REQUIRED_SSO_HOURS.
# recover deploys + tears down two full stacks sequentially; it needs a longer runway.
if [ "${SCENARIO:-}" = "recover" ]; then
  REQUIRED_SSO_HOURS="${REQUIRED_SSO_HOURS:-5}"
else
  REQUIRED_SSO_HOURS="${REQUIRED_SSO_HOURS:-3}"
fi
sso_seconds_left() { # <profile> -> seconds left on the SSO session token (-1 if unknown)
  command -v python3 >/dev/null 2>&1 || { echo -1; return; }
  python3 - "$1" <<'PY'
import configparser, glob, hashlib, json, os, sys
from datetime import datetime, timezone
profile = sys.argv[1]
cfg = configparser.ConfigParser()
cfg.read(os.path.expanduser("~/.aws/config"))
# The default profile may live under either header ("[default]" is canonical, but
# "[profile default]" is accepted by the CLI and exists in the wild - this exact
# mismatch once sent the check to the wrong token).
sects = [("default" if profile == "default" else "profile %s" % profile)]
if profile == "default":
    sects.append("profile default")
session = start = None
for sect in sects:
    if cfg.has_section(sect):
        session = cfg.get(sect, "sso_session", fallback=None)
        start = cfg.get(sect, "sso_start_url", fallback=None)
        break
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
# ONLY the profile's own start-URL token counts. The old fallback ("newest token in
# the cache") is how an expired commercial session hid behind a fresh gov token and a
# 4h run launched with 17 minutes of real runway - dying mid-flight exactly as this
# check exists to prevent. Unknown beats wrong: with no matching token, report
# unknown and let the operator decide.
best = newest([f for f in glob.glob(os.path.expanduser("~/.aws/sso/cache/*.json")) if f in targets])
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

# The harness only ever targets the DDVtest account - used for state/resource/
# artifacts-bucket names and passed to the tests as AccountID. Read from whichever
# account the profile lands in rather than carried as a literal in a public repo.
# Set DDVTEST_ACCOUNT_ID yourself to target another test account, or to skip the
# lookup. Runs after the SSO check so an expired session reports as an expired
# session rather than as an STS failure.
if [ -z "${DDVTEST_ACCOUNT_ID:-}" ]; then
  # The REQUIRED_BINS sweep is much further down, so check the one tool this needs here.
  # Without it a missing CLI reads as "could not resolve the account", which sends the
  # operator looking at their SSO session instead of at their PATH.
  command -v aws >/dev/null || {
    echo "missing required tool: aws (needed to resolve the test account id)" >&2
    echo "       Install it, or export DDVTEST_ACCOUNT_ID=<12-digit account> to skip." >&2
    exit 1
  }
  # stderr is kept, not discarded: an expired token or a denied sts:GetCallerIdentity says
  # exactly what is wrong, and swallowing it leaves only the generic message below. tr -d
  # strips the CR that some environments append, which the exact-width match would reject.
  _sts_err="$(mktemp)"
  DDVTEST_ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text \
    2>"$_sts_err" | tr -d '\r' || true)"
  if [ -z "$DDVTEST_ACCOUNT_ID" ] && [ -s "$_sts_err" ]; then
    echo "ERROR: sts:GetCallerIdentity failed for AWS_PROFILE='$AWS_PROFILE'." >&2
    sed '/^[[:space:]]*$/d; s/^/       aws: /' "$_sts_err" >&2
  fi
  rm -f "$_sts_err"
fi
# Validated unconditionally, not just on the resolved path. An explicit override is the
# documented escape hatch, and a typo in it would otherwise sail straight into every
# bucket name below and build a plausible-looking one that points nowhere.
case "$DDVTEST_ACCOUNT_ID" in
  [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;;
  *)
    echo "ERROR: DDVTEST_ACCOUNT_ID must be a 12-digit AWS account id (got '${DDVTEST_ACCOUNT_ID:-}')." >&2
    echo "       Refresh the profile, or export DDVTEST_ACCOUNT_ID=<12-digit account>." >&2
    exit 1
    ;;
esac
export DDVTEST_ACCOUNT_ID
echo ">> Test account: $DDVTEST_ACCOUNT_ID (profile '$AWS_PROFILE')"

VAULT_KUBE_CONTEXT="${VAULT_KUBE_CONTEXT:-vault-standard}"
VAULT_AWS_PROFILE="${VAULT_AWS_PROFILE:-dozuki}"
VAULT_AWS_ROLE="${VAULT_AWS_ROLE:-admin}"
# Exported so the Go harness's teardown can re-login to Vault with the same
# profile/role if the inherited token expired during a long run (see
# refreshVaultToken in harness/terragrunt.go).
export VAULT_AWS_PROFILE VAULT_AWS_ROLE
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
  [ -n "${AZ_SHIM_DIR:-}" ] && rm -rf "$AZ_SHIM_DIR" 2>/dev/null || true

  # Archive this run's artifacts to S3 for post-mortem. The harness writes diagnostics
  # to .artifacts/$RUN_ID BEFORE teardown (TF inventory, env.hcl, and — on failure — a
  # live-cluster dump: pods/events/failed-pod logs/gateway status/configmaps), which are
  # gone once the cluster + worktrees are torn down. Bundle that + the run log and upload.
  # Best-effort; opt out with SKIP_ARTIFACTS=1.
  if [ "${STARTED_RUN:-0}" = 1 ] && [ "${SKIP_ARTIFACTS:-0}" != 1 ]; then
    _adir="$PWD/.artifacts/$RUN_ID"
    mkdir -p "$_adir"
    cp -f "$RUN_LOG" "$_adir/run.log" 2>/dev/null || true
    _bundle="$PWD/.artifacts/${RUN_ID}.tar.gz"
    # The harness writes per-config diagnostics to .artifacts/<RUN_ID>-<config>/ (its
    # p.RunID includes the config name), so bundle those dirs too — not just the run-log
    # dir — or the upload is just the log. Feed the dir list to tar via -T (robust to
    # shell word-splitting).
    if ( cd "$PWD/.artifacts" && ls -d "$RUN_ID" "$RUN_ID"-* 2>/dev/null | tar -czf "$_bundle" -T - ) 2>/dev/null; then
      _bucket="${ARTIFACTS_BUCKET:-dozuki-cloudprem-harness-artifacts-us-east-1-${DDVTEST_ACCOUNT_ID}}"
      if aws s3 cp "$_bundle" "s3://${_bucket}/${RUN_ID}.tar.gz" --profile "$AWS_PROFILE" >/dev/null 2>&1; then
        echo ">> Artifacts archived: s3://${_bucket}/${RUN_ID}.tar.gz" >&2
      else
        echo ">> Artifacts: S3 upload failed (perms/SSO?); local bundle kept at $_bundle" >&2
      fi
    fi
  fi

  echo ">> Full run log saved to: $RUN_LOG" >&2
}
trap cleanup EXIT

setup_vault() {
  # The vault kube context AND the vault AWS-auth login both authenticate as
  # VAULT_AWS_PROFILE (dozuki — the management account the Vault cluster lives in).
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

# Presence is not enough. Terragrunt removed the --terragrunt-* flags in 0.85.0 and
# `run-all` in 0.84.0, with no deprecation grace period, and this harness now uses the
# post-0.78 CLI exclusively. An older terragrunt on PATH fails with a bare "flag
# provided but not defined" ~20 minutes into a run rather than up front. `live/` does
# carry a committed `.terragrunt-version`, but it only binds when terragrunt is launched
# from `live/` or below and only when the caller uses tgenv at all, so it is a default,
# not a guarantee. Assert the floor here regardless.
#
# The floor is 0.78 (where `run --all` and the unprefixed flags landed), NOT the
# version the fleet pins. Verified against real binaries: 0.99.4 accepts the new CLI
# and already REJECTS --terragrunt-*, so anything >= 0.78 runs this harness. Gating on
# the fleet pin instead would reject a working setup for no reason.
TG_MIN_MAJOR=0
TG_MIN_MINOR=78
tg_raw="$(terragrunt --version 2>/dev/null | head -1)"
tg_ver="$(printf '%s' "$tg_raw" | sed -nE 's/.*v?([0-9]+)\.([0-9]+)\.([0-9]+).*/\1 \2/p')"
if [ -z "$tg_ver" ]; then
  echo "WARNING: could not parse a terragrunt version from: $tg_raw" >&2
else
  tg_major="${tg_ver% *}"; tg_minor="${tg_ver#* }"
  if [ "$tg_major" -lt "$TG_MIN_MAJOR" ] || { [ "$tg_major" -eq "$TG_MIN_MAJOR" ] && [ "$tg_minor" -lt "$TG_MIN_MINOR" ]; }; then
    cat >&2 <<EOF
ERROR: terragrunt ${tg_major}.${tg_minor}.x is too old for this harness (need >= ${TG_MIN_MAJOR}.${TG_MIN_MINOR}).
  The harness uses the post-0.78 CLI (\`run --all\`, \`--non-interactive\`). An older
  binary fails with a bare "flag provided but not defined" partway into a run.
  Install the version the fleet pins, from OUTSIDE the repo:  cd ~ && tgenv install 1.1.2
  Then run from live/ and the committed live/.terragrunt-version selects it.
  Do not run 'tgenv use', or 'tgenv install', from inside the repo: both write whichever
  version file currently resolves ('install' calls 'use'), which from live/ is the
  committed pin - a tracked file - and from anywhere unpinned is the shared default that
  unpinned shells and worktrees fall back to.
EOF
    exit 1
  fi
  echo ">> terragrunt ${tg_major}.${tg_minor}.x — OK"
fi

# The engine version is NOT gated, only reported - but report it loudly, because a run on
# a different OpenTofu than Spacelift uses is evidence about a toolchain nobody deploys.
# One run got most of the way through on 1.12.2 while the fleet sat on 1.11.8, and nothing
# in the output said so. Not fatal: local experiments on a newer engine are legitimate, and
# a hard gate here would also strand anyone mid-upgrade. Keep these in step with
# infra-live/_spacelift/stacks.tf (terraform_version / terragrunt_version) - that file is
# the source of truth, this is a mirror.
FLEET_TOFU_VERSION="1.12.5"
FLEET_TG_VERSION="1.1.2"
tofu_raw="$("$TG_TF_PATH" version 2>/dev/null | head -1)"
tofu_ver="$(printf '%s' "$tofu_raw" | sed -nE 's/.*v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p')"
if [ "$tofu_ver" = "$FLEET_TOFU_VERSION" ]; then
  echo ">> OpenTofu ${tofu_ver} — matches the fleet pin"
else
  echo ">> WARNING: OpenTofu ${tofu_ver:-unknown} != fleet pin ${FLEET_TOFU_VERSION}. This run tests an" >&2
  echo ">>          engine Spacelift does not use.  Run 'tofuenv install ${FLEET_TOFU_VERSION}', then run" >&2
  echo ">>          from live/ so the committed live/.opentofu-version selects it." >&2
fi
if [ -n "${tg_ver:-}" ] && [ "$(printf '%s' "$tg_raw" | sed -nE 's/.*v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p')" != "$FLEET_TG_VERSION" ]; then
  echo ">> WARNING: terragrunt is not the fleet pin ${FLEET_TG_VERSION} (1.x double-assumes iam_role; infra-live #158)." >&2
fi

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

# az-env-gap guard: simulate Spacelift's shared workers (no usable Azure CLI) by
# shimming `az` to fail, so any AWS-path provider that depends on the Azure CLI breaks
# the harness HERE instead of only at real deploy time (this is the gap that hid the
# azurerm-on-AWS issue — the harness ran where `az` existed). On by default for AWS
# runs; NO_AZ_SHIM=1 disables. Cleaned up by the EXIT trap.
if [ "${NO_AZ_SHIM:-0}" != 1 ]; then
  AZ_SHIM_DIR="$(mktemp -d)"
  cat > "$AZ_SHIM_DIR/az" <<'AZEOF'
#!/usr/bin/env bash
echo ">> [harness] 'az' is intentionally shimmed to fail — simulating Spacelift workers (no Azure CLI). Set NO_AZ_SHIM=1 to disable." >&2
exit 1
AZEOF
  chmod +x "$AZ_SHIM_DIR/az"
  export PATH="$AZ_SHIM_DIR:$PATH"
  echo ">> az-env-gap guard ACTIVE: 'az' shimmed to fail (NO_AZ_SHIM=1 to disable)." >&2
else
  echo ">> az-env-gap guard DISABLED (NO_AZ_SHIM=1) — 'az' uses the ambient PATH." >&2
fi

# Optional PHASE mode: drive a SINGLE re-entrant phase via the harness CLI instead of
# the whole go-test scenario — this is exactly how Argo Workflows invokes each phase.
# State is shared across phases via the run manifest in S3 (<run-id>-<config>/
# harness-manifest.json, at the same prefix as TF state), so reuse the SAME RUN_ID for
# every phase of one run. Unset PHASE => unchanged full go-test path below (parity).
#   PHASE=provision SCENARIO_FLAG="--scenario upgrade" FROM_REF=v6.0.3 TO_REF=v7.1.0 \
#     CONFIGS=min_default ./run.sh
#   PHASE=upgrade  CONFIGS=min_default RUN_ID=<same-id> ./run.sh
#   PHASE=validate CONFIGS=min_default RUN_ID=<same-id> ./run.sh
#   PHASE=teardown CONFIGS=min_default RUN_ID=<same-id> ./run.sh   # +KEEP_ON_FAILURE=1
if [ -n "${PHASE:-}" ]; then
  REPO_ROOT="$(git -C "$PWD" rev-parse --show-toplevel)"
  # Manifest lives in the TF state bucket (live/root.hcl remote_state naming).
  STATE_BUCKET="${STATE_BUCKET:-${TG_BUCKET_PREFIX:-}dozuki-terraform-state-${REGION:-us-east-1}-${DDVTEST_ACCOUNT_ID}}"
  echo ">> PHASE mode: $PHASE (run-id=$RUN_ID config=${CONFIGS:-min_default} bucket=$STATE_BUCKET)" >&2
  BIN="$PWD/.bin/harness"
  mkdir -p "$PWD/.bin"
  go build -o "$BIN" ./cmd/harness
  _keep=""; [ "${KEEP_ON_FAILURE:-0}" = 1 ] && _keep="--keep-on-failure"
  "$BIN" "$PHASE" \
    --run-id "$RUN_ID" --config "${CONFIGS:-min_default}" \
    --repo-dir "$REPO_ROOT" \
    --account-id "$DDVTEST_ACCOUNT_ID" --profile "$AWS_PROFILE" \
    --region "${REGION:-us-east-1}" --state-bucket "$STATE_BUCKET" \
    --matrix "$PWD/matrix.yaml" \
    ${SCENARIO_FLAG:-} ${FROM_REF:+--from-ref "$FROM_REF"} ${TO_REF:+--to-ref "$TO_REF"} ${_keep}
  exit $?
fi

# Scenario selection: upgrade | fresh | both (default both) | recover. 'both' runs
# TestUpgrade then TestFresh in one go-test process; a failure in EITHER makes go test
# exit non-zero. 'recover' is the on-demand DR rebuild drill (two full stacks, ~4h) —
# deliberately not part of 'both'.
SCENARIO="${SCENARIO:-both}"
case "$SCENARIO" in
  upgrade) _run='TestUpgrade' ;;
  fresh)   _run='TestFresh' ;;
  both)    _run='TestUpgrade|TestFresh' ;;
  recover) _run='TestRecover' ;;
  *) echo ">> ERROR: invalid SCENARIO='$SCENARIO' (want upgrade|fresh|both|recover)" >&2; exit 2 ;;
esac
echo ">> Running scenario(s): ${SCENARIO}  (go test -run '${_run}')" >&2

# Compile the test to a STABLE binary path, then run THAT binary — instead of
# `go test`, which builds a throwaway scenarios.test at a fresh temp path every
# run. Host firewalls (e.g. Little Snitch) treat each new temp path as a new
# unknown process and re-prompt/deny it, silently blocking the in-process
# endpoint-health HTTP checks when no one is there to approve. A fixed path lets
# the allow-rule persist across runs (approve once). The binary is run from the
# scenarios/ dir so its CWD matches `go test ./scenarios/` (relative paths intact).
TEST_BIN="$PWD/.bin/scenarios.test"
mkdir -p "$PWD/.bin"
go test -c -o "$TEST_BIN" ./scenarios/
# recover deploys + tears down TWO full stacks sequentially; give it more headroom.
_timeout=180m
[ "$SCENARIO" = "recover" ] && _timeout=360m
if ( cd scenarios && "$TEST_BIN" -test.run "$_run" -test.v -test.timeout "$_timeout" ); then TEST_RC=0; else TEST_RC=$?; fi

exit "$TEST_RC"
