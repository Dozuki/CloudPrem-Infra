#!/usr/bin/env bash
# Server-side image sync: GHCR -> customer ACR via az acr import.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source lib/common.sh

PHYS_TFVARS="${KIT_ROOT}/physical.tfvars"
need_file "$PHYS_TFVARS" "physical.tfvars not found"
need_file "${KIT_ROOT}/images.lock" "images.lock not found"

ACR_ID="$(tfvar "$PHYS_TFVARS" acr_id)"
[[ -n "$ACR_ID" ]] || die "acr_id not set in physical.tfvars"
ACR_NAME="${ACR_ID##*/}"

# Optional pull credentials for private GHCR sources (provided with the bundle).
GHCR_USER="${GHCR_USER:-}"
GHCR_TOKEN="${GHCR_TOKEN:-}"

synced=0
while read -r source target; do
  [[ -z "$source" || "$source" == \#* ]] && continue
  log "importing ${source} -> ${ACR_NAME}/${target}"
  args=(acr import --name "$ACR_NAME" --source "$source" --image "$target" --force)
  if [[ -n "$GHCR_TOKEN" ]]; then
    args+=(--username "$GHCR_USER" --password "$GHCR_TOKEN")
  fi
  az "${args[@]}" --only-show-errors
  synced=$((synced + 1))
done < "${KIT_ROOT}/images.lock"

[[ $synced -gt 0 ]] || warn "images.lock contained no images (template not yet populated by release CI?)"
log "synced ${synced} image(s) into ${ACR_NAME}"
