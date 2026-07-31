#!/usr/bin/env bash
#
# In-cluster entrypoint for the harness image. This is the server-side half of run.sh:
# the same setup a phase needs, minus the laptop ergonomics (SSO runway checks, the Vault
# port-forward, the Little Snitch stable-binary workaround). run.sh stays the local
# launcher; this drives the identical `harness <phase>` subcommands from a pod.
#
# Usage:
#   docker-entrypoint.sh provision|upgrade|validate|teardown [flags...]   -> harness
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
case "${1:-}" in
  provision | upgrade | validate | teardown) BIN=harness ;;
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
REPO_REF="${HARNESS_REPO_REF:-master}"

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
  cat >"${HOME:-/root}/.aws/config" <<EOF
[profile ${_profile}]
role_arn = ${HARNESS_ASSUME_ROLE_ARN}
credential_source = EcsContainer
role_session_name = harness-${HARNESS_RUN_LABEL:-phase}
EOF
  log "AWS: profile ${_profile} chains into ${HARNESS_ASSUME_ROLE_ARN}"
  if ! aws --profile "${_profile}" sts get-caller-identity >/dev/null 2>&1; then
    log "ERROR: cannot assume ${HARNESS_ASSUME_ROLE_ARN} - check the role's trust policy admits this pod's role"
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
# Toolchain banner. A run is 1-2 hours; knowing which tofu/terragrunt produced a failure
# is worth the few hundred milliseconds, and a version drift from the fleet pin is the
# kind of thing that otherwise surfaces as a confusing mid-run error.
# ---------------------------------------------------------------------------
log "toolchain: $(tofu version | head -1) | terragrunt $(terragrunt --version | sed -nE 's/.*v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p' | head -1) | $(helm version --short 2>/dev/null) | kubectl $(kubectl version --client -o json 2>/dev/null | jq -r .clientVersion.gitVersion)"
log "provider cache: ${TF_PLUGIN_CACHE_DIR:-<unset>}"

exec "${BIN}" "$@"
