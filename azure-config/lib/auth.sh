#!/usr/bin/env bash
# Azure authentication + environment detection. Sourced after common.sh.

# Detect Azure VM managed identity via IMDS (169.254.169.254).
_on_azure_vm() {
  curl -fs -m 2 -H 'Metadata: true' \
    'http://169.254.169.254/metadata/instance?api-version=2021-02-01' >/dev/null 2>&1
}

azure_login() {
  local subscription="$1" env="$2" tenant="${3:-}"

  if [[ "$env" == "usgovernment" ]]; then
    az cloud set --name AzureUSGovernment >/dev/null
  else
    az cloud set --name AzureCloud >/dev/null
  fi

  if az account show >/dev/null 2>&1; then
    log "already logged in as $(az account show --query user.name -o tsv)"
  elif _on_azure_vm; then
    log "Azure VM detected - logging in with managed identity"
    # az login --identity does not accept --tenant; the VM identity pins the tenant.
    az login --identity >/dev/null
  else
    log "opening device-code login (share this code on the call)"
    if [[ -n "$tenant" ]]; then
      az login --use-device-code --tenant "$tenant" >/dev/null
    else
      az login --use-device-code >/dev/null
    fi
  fi

  az account set --subscription "$subscription"
  log "subscription: $(az account show --query name -o tsv) (${subscription})"
}

# Best-effort public egress IP (for KV/AKS firewall allowlists).
egress_ip() {
  local ip
  for url in "https://api.ipify.org" "https://ifconfig.me/ip" "https://icanhazip.com"; do
    ip="$(curl -fsS -m 5 "$url" 2>/dev/null | tr -d '[:space:]')" || continue
    [[ "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] && { printf '%s' "$ip"; return 0; }
  done
  return 1
}
