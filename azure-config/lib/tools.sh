#!/usr/bin/env bash
# Downloads pinned CLI tools into .bin/ with checksum verification.
# Sourced by bootstrap.sh after lib/common.sh.

TERRAFORM_VERSION="1.13.4"
KUBELOGIN_VERSION="v0.2.10"
KUBECTL_VERSION="v1.33.4"
HELM_VERSION="v3.19.0"
JQ_VERSION="1.7.1"

ensure_tools() {
  mkdir -p "${BIN_DIR}"
  local os arch
  read -r os arch <<<"$(os_arch)"

  command -v az >/dev/null 2>&1 || die "Azure CLI (az) is required. Install: https://learn.microsoft.com/cli/azure/install-azure-cli"
  command -v unzip >/dev/null 2>&1 || die "unzip is required (apt-get install unzip / yum install unzip)"

  _ensure_terraform "$os" "$arch"
  _ensure_kubelogin "$os" "$arch"
  _ensure_kubectl "$os" "$arch"
  _ensure_helm "$os" "$arch"
  _ensure_jq "$os" "$arch"
  log "tools ready: $(terraform version -json | "${BIN_DIR}/jq" -r .terraform_version 2>/dev/null || terraform version | head -1)"
}

_fetch() { # url dest
  curl -fsSL --retry 3 -o "$2" "$1" || die "download failed: $1"
}

_verify() { # file expected_sha
  local got; got="$(sha256_of "$1")"
  [[ "$got" == "$2" ]] || die "checksum mismatch for $1 (got $got, want $2)"
}

_ensure_terraform() {
  # Note: grep -q on the pipe trips pipefail (SIGPIPE on terraform's multi-line
  # output), so capture the first line instead. CHECKPOINT_DISABLE skips the
  # upstream version-check network call.
  if [[ -x "${BIN_DIR}/terraform" ]]; then
    local have
    have="$(CHECKPOINT_DISABLE=1 "${BIN_DIR}/terraform" version 2>/dev/null | head -n1)" || true
    [[ "$have" == "Terraform v${TERRAFORM_VERSION}" ]] && return 0
  fi
  local os=$1 arch=$2 base="https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}"
  local zip="terraform_${TERRAFORM_VERSION}_${os}_${arch}.zip" tmp; tmp="$(mktemp -d)"
  _fetch "${base}/${zip}" "${tmp}/${zip}"
  _fetch "${base}/terraform_${TERRAFORM_VERSION}_SHA256SUMS" "${tmp}/SUMS"
  local want; want="$(awk -v f="$zip" '$2==f{print $1}' "${tmp}/SUMS")"
  [[ -n "$want" ]] || die "no checksum for ${zip} in SHA256SUMS"
  _verify "${tmp}/${zip}" "$want"
  unzip -oq "${tmp}/${zip}" -d "${BIN_DIR}" && rm -rf "$tmp"
  log "installed terraform ${TERRAFORM_VERSION}"
}

_ensure_kubelogin() {
  [[ -x "${BIN_DIR}/kubelogin" ]] && return 0
  local os=$1 arch=$2 tmp; tmp="$(mktemp -d)"
  local zip="kubelogin-${os}-${arch}.zip"
  local base="https://github.com/Azure/kubelogin/releases/download/${KUBELOGIN_VERSION}"
  _fetch "${base}/${zip}" "${tmp}/${zip}"
  _fetch "${base}/${zip}.sha256" "${tmp}/${zip}.sha256"
  _verify "${tmp}/${zip}" "$(awk '{print $1}' "${tmp}/${zip}.sha256")"
  unzip -oq "${tmp}/${zip}" -d "${tmp}"
  install -m 0755 "${tmp}/bin/${os}_${arch}/kubelogin" "${BIN_DIR}/kubelogin" && rm -rf "$tmp"
  log "installed kubelogin ${KUBELOGIN_VERSION}"
}

_ensure_kubectl() {
  [[ -x "${BIN_DIR}/kubectl" ]] && return 0
  local os=$1 arch=$2 tmp; tmp="$(mktemp -d)"
  local base="https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${os}/${arch}"
  _fetch "${base}/kubectl" "${tmp}/kubectl"
  _fetch "${base}/kubectl.sha256" "${tmp}/kubectl.sha256"
  _verify "${tmp}/kubectl" "$(cat "${tmp}/kubectl.sha256")"
  install -m 0755 "${tmp}/kubectl" "${BIN_DIR}/kubectl" && rm -rf "$tmp"
  log "installed kubectl ${KUBECTL_VERSION}"
}

_ensure_helm() {
  [[ -x "${BIN_DIR}/helm" ]] && return 0
  local os=$1 arch=$2 tmp; tmp="$(mktemp -d)"
  local tar="helm-${HELM_VERSION}-${os}-${arch}.tar.gz"
  _fetch "https://get.helm.sh/${tar}" "${tmp}/${tar}"
  _fetch "https://get.helm.sh/${tar}.sha256sum" "${tmp}/${tar}.sha256sum"
  _verify "${tmp}/${tar}" "$(awk '{print $1}' "${tmp}/${tar}.sha256sum")"
  tar -xzf "${tmp}/${tar}" -C "${tmp}"
  install -m 0755 "${tmp}/${os}-${arch}/helm" "${BIN_DIR}/helm" && rm -rf "$tmp"
  log "installed helm ${HELM_VERSION}"
}

_ensure_jq() {
  [[ -x "${BIN_DIR}/jq" ]] && return 0
  local os=$1 arch=$2 tmp; tmp="$(mktemp -d)"
  local osname=$os; [[ "$os" == "darwin" ]] && osname=macos
  local bin="jq-${osname}-${arch}"
  local base="https://github.com/jqlang/jq/releases/download/jq-${JQ_VERSION}"
  _fetch "${base}/${bin}" "${tmp}/jq"
  _fetch "${base}/sha256sum.txt" "${tmp}/sums"
  _verify "${tmp}/jq" "$(awk -v f="$bin" '$2==f{print $1}' "${tmp}/sums")"
  install -m 0755 "${tmp}/jq" "${BIN_DIR}/jq" && rm -rf "$tmp"
  log "installed jq ${JQ_VERSION}"
}
