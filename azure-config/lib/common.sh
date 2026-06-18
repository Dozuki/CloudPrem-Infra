#!/usr/bin/env bash
# Shared helpers for the MPC Azure deploy kit. Sourced, not executed.

set -euo pipefail

KIT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${KIT_ROOT}/.bin"
# shellcheck disable=SC2034
STATE_DIR="${KIT_ROOT}/.state"
PHYSICAL_DIR="${KIT_ROOT}/terraform/physical-azure"
LOGICAL_DIR="${KIT_ROOT}/terraform/logical"

# In-repo development layout: terraform/ sits beside azure-config/, not inside it.
if [[ ! -d "${PHYSICAL_DIR}" && -d "${KIT_ROOT}/../terraform/physical-azure" ]]; then
  PHYSICAL_DIR="$(cd "${KIT_ROOT}/../terraform/physical-azure" && pwd)"
  # shellcheck disable=SC2034
  LOGICAL_DIR="$(cd "${KIT_ROOT}/../terraform/logical" && pwd)"
fi

export PATH="${BIN_DIR}:${PATH}"

log()  { printf '\033[1;34m[mpc]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[mpc]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[mpc]\033[0m ERROR: %s\n' "$*" >&2; exit 1; }

need_file() { [[ -f "$1" ]] || die "$2"; }

os_arch() {
  local os arch
  case "$(uname -s)" in
    Linux)  os=linux ;;
    Darwin) os=darwin ;;
    *) die "unsupported OS: $(uname -s)" ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
  printf '%s %s' "$os" "$arch"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# tfvar <file> <key> — extract a simple string/number value from a tfvars file.
tfvar() {
  awk -F'=' -v k="$2" '
    $0 !~ /^[[:space:]]*#/ && $1 ~ "^[[:space:]]*"k"[[:space:]]*$" {
      v=$2; gsub(/^[[:space:]]*"?|"?[[:space:]]*(#.*)?$/, "", v); print v; exit
    }' "$1"
}
