#!/usr/bin/env bash
# 30-second health summary, designed to be read aloud on a screen-share call.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source lib/common.sh

PHYS_TFVARS="${KIT_ROOT}/physical.tfvars"
need_file "$PHYS_TFVARS" "physical.tfvars not found"
CUSTOMER="$(tfvar "$PHYS_TFVARS" customer)"
ENVIRONMENT="$(tfvar "$PHYS_TFVARS" environment)"
IDENT="${CUSTOMER}-${ENVIRONMENT}"
RG="${IDENT}-mpc"
mkdir -p "${STATE_DIR}"

section() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

section "Azure resources (${RG})"
az resource list --resource-group "$RG" --query '[].{name:name,type:type}' -o table 2>/dev/null \
  || warn "resource group ${RG} not found (physical layer not applied?)"

section "MySQL"
az mysql flexible-server list --resource-group "$RG" \
  --query '[].{name:name,state:state,haState:highAvailability.state,version:version}' -o table 2>/dev/null || true

section "Cluster"
if az aks get-credentials --resource-group "$RG" --name "${IDENT}-aks" \
     --overwrite-existing --file "${STATE_DIR}/kubeconfig" >/dev/null 2>&1; then
  export KUBECONFIG="${STATE_DIR}/kubeconfig"
  kubelogin convert-kubeconfig -l azurecli --kubeconfig "$KUBECONFIG" 2>/dev/null || true
  kubectl get nodes -o wide 2>/dev/null || warn "cannot reach cluster API (allowlist? group membership?)"
  section "Workloads (dozuki namespace)"
  kubectl get pods -n dozuki -o wide 2>/dev/null | head -40 || true
  section "Ingress"
  kubectl get svc -n envoy-gateway-system dozuki-envoy-proxy \
    -o jsonpath='LoadBalancer IP: {.status.loadBalancer.ingress[0].ip}{"\n"}' 2>/dev/null \
    || warn "ingress service not found (logical layer not applied?)"
else
  warn "AKS cluster ${IDENT}-aks not reachable"
fi

section "Recent events (warnings)"
kubectl get events -n dozuki --field-selector type=Warning \
  --sort-by=.lastTimestamp 2>/dev/null | tail -10 || true
