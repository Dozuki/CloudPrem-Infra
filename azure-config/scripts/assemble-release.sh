#!/usr/bin/env bash
# Assembles the self-contained customer bundle. Run from anywhere inside the
# repo; CI runs it on tag. Output: dist/cloudprem-azure-<version>.tar.gz
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
VERSION="${1:-dev}"
DIST="${REPO_ROOT}/dist"
NAME="cloudprem-azure-${VERSION}"
STAGE="${DIST}/${NAME}"

rm -rf "${STAGE}" && mkdir -p "${STAGE}/terraform"

# 1) Deploy kit (scripts + examples), excluding dev-only artifacts.
rsync -a "${REPO_ROOT}/azure-config/" "${STAGE}/" \
  --exclude '.bin' --exclude '.state' --exclude 'scripts' --exclude '*.tfvars'

# 2) Vendored terraform layers. The chart submodule must be materialized.
git -C "${REPO_ROOT}" submodule update --init --recursive
rsync -a "${REPO_ROOT}/terraform/physical-azure/" "${STAGE}/terraform/physical-azure/" \
  --exclude '.terraform' --exclude '.terraform.lock.hcl' --exclude 'examples'
rsync -a "${REPO_ROOT}/terraform/logical/" "${STAGE}/terraform/logical/" \
  --exclude '.terraform' --exclude '.terraform.lock.hcl' --exclude 'examples' \
  --exclude '.terragrunt-cache' --exclude 'backend_override.tf' --exclude 'aws_stub_override.tf' \
  --exclude 'charts/dozuki/.idea' --exclude 'charts/dozuki/utils' \
  --exclude 'charts/dozuki/.claude' --exclude 'charts/dozuki/CODEOWNERS'
cp "${REPO_ROOT}/terraform/CONTRACT.md" "${STAGE}/terraform/CONTRACT.md"

# Strip submodule git metadata so the bundle is plain files.
rm -rf "${STAGE}/terraform/logical/charts/dozuki/.git"
find "${STAGE}" -name '.gitmodules' -delete

# 3) Self-containment guard: nothing may reference internal repos or AWS-hosted
# artifact endpoints (artifact rule: no deploy-time AWS domains).
if grep -RInE 'git@github.com:Dozuki/(CloudPrem-Infra|helm)|\.dkr\.ecr\.|s3\.amazonaws\.com/[a-z]' \
     "${STAGE}/terraform" --include='*.tf' --include='*.yaml' --include='*.hcl'; then
  echo "ERROR: bundle references internal or AWS-hosted artifacts" >&2
  exit 1
fi

# 4) Offline sanity: both layers must terraform-init/validate inside the bundle.
for layer in physical-azure logical; do
  ( cd "${STAGE}/terraform/${layer}" \
    && VAULT_ADDR=http://dummy terraform init -backend=false -input=false >/dev/null \
    && VAULT_ADDR=http://dummy terraform validate >/dev/null ) \
    || { echo "ERROR: bundle validate failed for ${layer}" >&2; exit 1; }
  rm -rf "${STAGE}/terraform/${layer}/.terraform" "${STAGE}/terraform/${layer}/.terraform.lock.hcl"
done

tar -czf "${DIST}/${NAME}.tar.gz" -C "${DIST}" "${NAME}"
( cd "${DIST}" && { sha256sum "${NAME}.tar.gz" > "${NAME}.tar.gz.sha256" 2>/dev/null \
  || shasum -a 256 "${NAME}.tar.gz" > "${NAME}.tar.gz.sha256"; } )
echo "bundle: ${DIST}/${NAME}.tar.gz"
