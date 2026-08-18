#!/usr/bin/env bash
#
# In-cluster entrypoint for the harness image. This is the server-side half of run.sh:
# the same setup a phase needs, minus the laptop ergonomics (SSO runway checks, the Vault
# port-forward, the Little Snitch stable-binary workaround). run.sh stays the local
# launcher; this drives the identical `harness <phase>` subcommands from a pod.
#
# Usage:
#   docker-entrypoint.sh provision|upgrade|validate|teardown|janitor|reaper-worker|reaper-drain-cancelled [flags...] -> harness
#   docker-entrypoint.sh cpi-dr [flags...]                               -> DR CLI
#   docker-entrypoint.sh bash|sh|<anything else>                         -> exec as given
set -euo pipefail

log() { echo ">> [entrypoint] $*" >&2; }

# ---------------------------------------------------------------------------
# az-env-gap guard. The shim is baked at /opt/az-shim and prepended to PATH by the
# image, so it is active by default. NO_AZ_SHIM=1 drops it.
# ---------------------------------------------------------------------------
if [ "${NO_AZ_SHIM:-0}" = 1 ]; then
  PATH="$(printf '%s' "$PATH" | sed -e 's|/opt/az-shim:||g')"
  export PATH
  log "az-env-gap guard DISABLED (NO_AZ_SHIM=1)."
fi

# Passthrough for anything that is not a harness phase, so the image doubles as a debug
# shell and as the host for cpi-dr and the cleanup backstops.
#
# Janitor and reaper-worker ride the same path as a phase on purpose. The report-only
# janitor needs the DDVtest profile chain to read S3/tags/locks; the queue-controlled
# worker can call the same Teardown a phase pod uses, so it also needs the repository and
# Vault token. One setup path makes credential failures surface in shadow report cycles
# before Resource Reaper actions are enabled.
case "${1:-}" in
  provision | upgrade | validate | teardown | janitor | reaper-worker | reaper-drain-cancelled) BIN=harness ;;
  cpi-dr)
    shift
    exec cpi-dr "$@"
    ;;
  "" | --help | -h)
    exec harness --help
    ;;
  *) exec "$@" ;;
esac

# ---------------------------------------------------------------------------
# Repo. The harness builds a git worktree per ref for dual-ref upgrades, so it needs a
# real clone with tags and origin/* remote-tracking refs - not a shallow snapshot and not
# a bare repo. It also reads live/tests/matrix.yaml relative to the repo, which is why
# this is a checkout rather than --no-checkout.
#
# Mount HARNESS_REPO_DIR as a volume shared by every phase of one run and this becomes an
# incremental fetch instead of a clone. In Argo that is a workflow-scoped volume.
# ---------------------------------------------------------------------------
REPO_DIR="${HARNESS_REPO_DIR:-/workspace/repo}"
# "auto" is the default every Argo entry point now passes, and it means "the caller has
# no opinion". Only teardown can act on that (it has a run manifest to ask); every other
# phase resolves it to master exactly as before. An explicit value is an override and is
# reported as a deviation below rather than silently obeyed, because forgetting the
# frozen ref on a teardown is invisible: the config re-resolves at whatever ref is
# checked out and the destroy then targets a plausible-looking wrong stack.
HARNESS_REPO_REF="${HARNESS_REPO_REF:-auto}"
if [ "${HARNESS_REPO_REF}" = auto ]; then
  REPO_REF=master
  REPO_REF_IS_AUTO=1
else
  REPO_REF="${HARNESS_REPO_REF}"
  REPO_REF_IS_AUTO=0
fi

if [ -d "${REPO_DIR}/.git" ]; then
  log "repo present at ${REPO_DIR}; fetching"
  git -C "${REPO_DIR}" fetch --tags --prune --quiet origin
else
  log "cloning ${HARNESS_REPO_URL} -> ${REPO_DIR}"
  mkdir -p "$(dirname "${REPO_DIR}")"
  git clone --quiet "${HARNESS_REPO_URL}" "${REPO_DIR}"
fi

# Detached on purpose: a phase must never depend on local branch state, and teardown of a
# failed run re-enters here against the same ref.
git -C "${REPO_DIR}" checkout --quiet --detach "origin/${REPO_REF}" 2>/dev/null \
  || git -C "${REPO_DIR}" checkout --quiet --detach "${REPO_REF}"
log "repo at $(git -C "${REPO_DIR}" rev-parse --short HEAD) (${REPO_REF})"

cd "${REPO_DIR}"

# ---------------------------------------------------------------------------
# Manual-TLS inputs. The physical layer ignores these; the logical layer's manual-TLS
# path needs a cert that is a real x509 pair, and the gateway secret must be
# kubernetes.io/tls. Cheap enough to mint per phase, and a throwaway stack's cert has no
# reason to outlive the run. Preset TF_VAR_tls_cert to supply your own.
# ---------------------------------------------------------------------------
if [ -z "${TF_VAR_tls_cert:-}" ]; then
  _tlsdir="$(mktemp -d)"
  openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
    -keyout "${_tlsdir}/tls.key" -out "${_tlsdir}/tls.crt" \
    -subj "/CN=harness.invalid" >/dev/null 2>&1
  TF_VAR_tls_cert="$(base64 -w0 <"${_tlsdir}/tls.crt")"
  TF_VAR_tls_key="$(base64 -w0 <"${_tlsdir}/tls.key")"
  export TF_VAR_tls_cert TF_VAR_tls_key
  log "TLS: generated a self-signed cert for the manual-TLS path"
fi

# ---------------------------------------------------------------------------
# Vault FIRST, before the DDVtest profile exists.
#
# Two different AWS identities are in play and mixing them breaks both. Vault AWS-auth
# must see the POD's own identity (the Argo workflow role, registered as a Vault role),
# while terragrunt must act as the assumed DDVtest role. run.sh keeps them apart with a
# subshell; here the ordering does it - the login runs while the pod's native Pod Identity
# credentials are still the only ones configured.
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Vault. Every terragrunt apply and destroy reads Vault, so a missing token fails ~20
# minutes in rather than up front.
#
# The difference from run.sh: no port-forward and no second SSO profile. This cluster
# reaches vault.internal.dozuki.com over the PrivateLink endpoint in its own VPC, and the
# pod's own identity is what Vault AWS-auth authenticates, so the login is direct.
#
# The token is only the STARTING token. It has a 1 hour TTL and the harness renews it
# in-process (StartVaultTokenRenewer) because a recovery or upgrade run always outlives
# the hour.
# ---------------------------------------------------------------------------
if [ -n "${VAULT_ADDR:-}" ] && [ -z "${VAULT_TOKEN:-}" ] && [ "${SKIP_VAULT_LOGIN:-0}" != 1 ]; then
  VAULT_AWS_ROLE="${VAULT_AWS_ROLE:-admin}"
  log "Vault: aws login (addr=${VAULT_ADDR} role=${VAULT_AWS_ROLE})"
  # region= matters for cross-partition STS; GovCloud must sign for us-gov-west-1.
  _login_args=(-method=aws "role=${VAULT_AWS_ROLE}")
  if [ -n "${VAULT_AWS_REGION:-}" ]; then
    _login_args+=("region=${VAULT_AWS_REGION}")
  fi
  VAULT_TOKEN="$(vault login -format=json "${_login_args[@]}" | jq -r '.auth.client_token')"
  [ -n "${VAULT_TOKEN}" ] && [ "${VAULT_TOKEN}" != "null" ] || {
    log "ERROR: vault aws login returned no token"
    exit 1
  }
  export VAULT_TOKEN VAULT_AWS_ROLE
  log "Vault: token acquired"
elif [ -n "${VAULT_TOKEN:-}" ]; then
  log "Vault: using the token supplied in the environment"
fi

# ---------------------------------------------------------------------------
# Cross-account identity for the test stack.
#
# The pod's own role lives in the management account; the throwaway stack it builds lives
# in DDVtest. Rather than teaching the harness to chain roles, describe the chain to the
# SDK once: a named profile whose credential_source is the Pod Identity endpoint that EKS
# already injected. Every tool then assumes the DDVtest role with no code change - the
# aws CLI, the aws provider under tofu, and the harness's own
# `aws configure export-credentials --profile` path, which exists for exactly this.
#
# Set HARNESS_ASSUME_ROLE_ARN and pass `--profile ddvtest` to the phase.
# ---------------------------------------------------------------------------
if [ -n "${HARNESS_ASSUME_ROLE_ARN:-}" ]; then
  _profile="${HARNESS_ASSUME_PROFILE:-ddvtest}"
  mkdir -p "${HOME:-/root}/.aws"
  # credential_source=EcsContainer reads AWS_CONTAINER_CREDENTIALS_FULL_URI, which is what
  # EKS Pod Identity injects. It is mutually exclusive with source_profile, and the SDK
  # rejects the pair, so this is deliberately the only key here.
  # The run label IS the run id, and the run id addresses Terraform state: phases.go builds
  # the state prefix as RunID + "-" + ConfigName + "/" (see janitor.go's reverse of it). So a
  # malformed run id is never merely cosmetic - it points the run at a state prefix that does
  # not exist, which would destroy nothing while reporting success. Reject it loudly here
  # rather than sanitizing it, because sanitizing a state address silently changes WHICH
  # stack a teardown targets. Whitespace is the failure seen in the wild: a submitter passed
  # "<run_id> <config_name>" space-joined as a single --run-id.
  case "${HARNESS_RUN_LABEL:-phase}" in
    *[!A-Za-z0-9._-]*)
      log "ERROR: HARNESS_RUN_LABEL '${HARNESS_RUN_LABEL}' is not a valid run id"
      log "ERROR: allowed characters are A-Z a-z 0-9 . _ - and nothing else (no spaces)"
      log "ERROR: the run id addresses Terraform state as <run-id>-<config>/ - a malformed"
      log "ERROR: one would target a nonexistent prefix and tear down nothing. If you meant"
      log "ERROR: to pass a config name, pass it as --config, not joined onto --run-id."
      exit 1 ;;
  esac
  # Even with a valid run id, role_session_name is not free-form: STS enforces [\w+=,.@-]*
  # and a 64-char cap, and nothing here previously enforced the length. Clip to 56 so the
  # "harness-" prefix keeps the whole value inside 64. The tr is belt-and-braces for the
  # character class now that the case above rejects the realistic offenders.
  _session="harness-$(printf '%s' "${HARNESS_RUN_LABEL:-phase}" | tr -c '[:alnum:]_+=,.@-' '-' | cut -c1-56)"
  cat >"${HOME:-/root}/.aws/config" <<EOF
[profile ${_profile}]
role_arn = ${HARNESS_ASSUME_ROLE_ARN}
credential_source = EcsContainer
role_session_name = ${_session}
EOF
  log "AWS: profile ${_profile} chains into ${HARNESS_ASSUME_ROLE_ARN} (session ${_session})"
  # Capture stderr instead of discarding it. This check used to run with >/dev/null 2>&1
  # and print a hardcoded guess about the trust policy, which sent a real incident down
  # the wrong path for hours: the actual error was a one-line ValidationError on the
  # session name. Whatever the AWS CLI says, say it.
  if ! _sts_err=$(aws --profile "${_profile}" sts get-caller-identity 2>&1 >/dev/null); then
    log "ERROR: cannot assume ${HARNESS_ASSUME_ROLE_ARN} using session name '${_session}'"
    log "ERROR: aws returned: ${_sts_err}"
    log "ERROR: if that is a ValidationError, the run label is malformed; if AccessDenied, check the role's trust policy admits this pod's role"
    exit 1
  fi
  log "AWS: assumed $(aws --profile "${_profile}" sts get-caller-identity --query Arn --output text)"
  # Ambient identity must ALSO be the chained role, not the raw pod identity. The
  # kubernetes/helm/kubectl providers authenticate through an exec plugin
  # (`aws eks get-token`, no --profile), which reads only the ambient env; without this
  # the token carries the pod's mgmt-account role, which has no access entry on the
  # freshly built cluster, and every cluster-API resource dies 401 Unauthorized.
  # Exported AFTER vault login (above) on purpose: Vault AWS-auth must see the pod's
  # own identity, and this line is what would break that if it moved earlier.
  export AWS_PROFILE="${_profile}"
fi

# ---------------------------------------------------------------------------
# Teardown ref resolution (Lodestar-1xm.36.5).
#
# A teardown re-resolves the matrix config, the env path and the identifier against
# whatever ref this pod checked out - so tearing a run down from master when it was
# built at a frozen ref resolves a same-named config to different targets, and the
# destroy reports success against a stack it never touched. The run manifest already
# records the right ref (AppliedRef, or ToRef when a provision died before recording
# one), so ask it instead of relying on the submitter to remember.
#
# Runs here, after the AWS profile exists, because the manifest lives in S3. Everything
# about it is best-effort: `harness teardown-ref` exits 0 and prints nothing when it
# cannot answer, and a failed re-checkout leaves the original checkout in place. A
# teardown that runs against the wrong ref is bad; a teardown that refuses to run at all
# is worse.
# ---------------------------------------------------------------------------
if [ "${1:-}" = teardown ]; then
  _tdref_run=""; _tdref_cfg=""; _tdref_bucket=""; _tdref_region=""; _tdref_profile=""
  _prev=""
  # Both spellings. Go's flag package accepts --flag=value as readily as --flag value,
  # and a scan that only understood the second would silently find nothing, skip the
  # resolution and fall back to master - which is the exact silent-wrong-ref failure
  # this block exists to remove.
  for _arg in "$@"; do
    case "${_arg}" in
      --run-id=*)       _tdref_run="${_arg#*=}" ;;
      --config=*)       _tdref_cfg="${_arg#*=}" ;;
      --state-bucket=*) _tdref_bucket="${_arg#*=}" ;;
      --region=*)       _tdref_region="${_arg#*=}" ;;
      --profile=*)      _tdref_profile="${_arg#*=}" ;;
    esac
    case "${_prev}" in
      --run-id) _tdref_run="${_arg}" ;;
      --config) _tdref_cfg="${_arg}" ;;
      --state-bucket) _tdref_bucket="${_arg}" ;;
      --region) _tdref_region="${_arg}" ;;
      --profile) _tdref_profile="${_arg}" ;;
    esac
    _prev="${_arg}"
  done
  _manifest_ref=""
  if [ -n "${_tdref_run}" ] && [ -n "${_tdref_cfg}" ] && [ -n "${_tdref_bucket}" ]; then
    _manifest_ref="$(harness teardown-ref \
      --run-id "${_tdref_run}" --config "${_tdref_cfg}" \
      --state-bucket "${_tdref_bucket}" \
      ${_tdref_region:+--region "${_tdref_region}"} \
      ${_tdref_profile:+--profile "${_tdref_profile}"} || true)"
    _manifest_ref="$(printf '%s' "${_manifest_ref}" | tr -d '[:space:]')"
  fi
  if [ -z "${_manifest_ref}" ]; then
    log "teardown ref: manifest has no recorded ref; staying on ${REPO_REF}"
  elif [ "${REPO_REF_IS_AUTO}" = 1 ]; then
    if [ "${_manifest_ref}" != "$(git -C "${REPO_DIR}" rev-parse HEAD)" ]; then
      log "teardown ref: resolving to the manifest's recorded ref ${_manifest_ref} (was ${REPO_REF})"
      git -C "${REPO_DIR}" checkout --quiet --detach "origin/${_manifest_ref}" 2>/dev/null \
        || git -C "${REPO_DIR}" checkout --quiet --detach "${_manifest_ref}" 2>/dev/null \
        || log "WARNING: teardown ref: could not check out ${_manifest_ref}; staying on ${REPO_REF}"
      cd "${REPO_DIR}"
      log "repo at $(git -C "${REPO_DIR}" rev-parse --short HEAD) (${_manifest_ref})"
    fi
  elif [ "${_manifest_ref}" != "${REPO_REF}" ]; then
    log "DEVIATION: teardown ref: caller pinned repo-ref=${REPO_REF} but the manifest records ${_manifest_ref}; honoring the caller"
  fi
fi

# ---------------------------------------------------------------------------
# Toolchain banner. A run is 1-2 hours; knowing which tofu/terragrunt produced a failure
# is worth the few hundred milliseconds, and a version drift from the fleet pin is the
# kind of thing that otherwise surfaces as a confusing mid-run error.
# ---------------------------------------------------------------------------
log "toolchain: $(tofu version | head -1) | terragrunt $(terragrunt --version | sed -nE 's/.*v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p' | head -1) | $(helm version --short 2>/dev/null) | kubectl $(kubectl version --client -o json 2>/dev/null | jq -r .clientVersion.gitVersion)"
log "provider cache: ${TF_PLUGIN_CACHE_DIR:-<unset>}"

exec "${BIN}" "$@"
