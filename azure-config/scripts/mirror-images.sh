#!/usr/bin/env bash
# Mirrors MPC release images from ECR to GHCR (registry-to-registry; no local
# pull). Run manually at release time:
#   ./mirror-images.sh <app_tag> <nextjs_tag> [operator_tag]
# operator_tag defaults to 3.0.3 (the bundled dozuki-operator subchart appVersion).
# Requires: crane (brew install crane), aws CLI with ECR-read creds, gh CLI
# authenticated with write:packages (gh auth refresh -s write:packages).
set -euo pipefail

ECR_REGISTRY="069174876992.dkr.ecr.us-east-1.amazonaws.com"
ECR_REGION="us-east-1"
GHCR_NAMESPACE="ghcr.io/dozuki"

usage() { echo "usage: $0 <app_tag> <nextjs_tag> [operator_tag]" >&2; exit 1; }
[[ $# -ge 2 && $# -le 3 ]] || usage
APP_TAG="$1"
NEXTJS_TAG="$2"
OPERATOR_TAG="${3:-3.0.3}"

command -v crane >/dev/null 2>&1 || { echo "crane is required: brew install crane" >&2; exit 1; }
command -v aws   >/dev/null 2>&1 || { echo "aws CLI is required" >&2; exit 1; }
command -v gh    >/dev/null 2>&1 || { echo "gh CLI is required" >&2; exit 1; }

echo "[mirror] logging into ${ECR_REGISTRY}"
aws ecr get-login-password --region "${ECR_REGION}" \
  | crane auth login "${ECR_REGISTRY}" --username AWS --password-stdin

echo "[mirror] logging into ghcr.io"
gh auth token | crane auth login ghcr.io --username "$(gh api user -q .login)" --password-stdin

for image in "app:${APP_TAG}" "web-nextjs:${NEXTJS_TAG}" "dozuki-operator:${OPERATOR_TAG}"; do
  src="${ECR_REGISTRY}/${image}"
  dst="${GHCR_NAMESPACE}/${image}"
  echo "[mirror] ${src} -> ${dst}"
  crane copy "${src}" "${dst}"
done

cat <<EOF

[mirror] done. images now pullable by MPC deployments:
  ghcr.io/dozuki/app:${APP_TAG}
  ghcr.io/dozuki/web-nextjs:${NEXTJS_TAG}
  ghcr.io/dozuki/dozuki-operator:${OPERATOR_TAG}
EOF
