# OpenTofu, not Terraform: physical requires >= 1.11.1 and logical needs OpenTofu's
# provider for_each, so the old tf-1.5.2 image could not init either layer.
#
# Pinned to the exact pair the Spacelift fleet runs. Terragrunt 1.1.1 double-assumed
# iam_role and 403'd at init on every stack that has one (infra-live #158); 1.1.2
# fixed the backend double-assume, proven on the gov parker canary on 2026-08-05
# before the fleet moved to it (infra-live #235).
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

ARG TOFU_VERSION=1.12.5
ARG TERRAGRUNT_VERSION=1.1.2
ARG BUILDPLATFORM
ARG TARGETPLATFORM
ARG TARGETARCH

ENV KUBE_VERSION="v1.32.0"

# Build the exact fleet toolchain instead of inheriting the third-party image's nearest
# available OpenTofu patch. Every downloaded binary is checksum-verified for the
# BuildKit target architecture.
RUN set -eux \
    && apk add --no-cache aws-cli bash ca-certificates curl git jq openssh-client unzip \
    && tool_arch="${TARGETARCH:-amd64}" \
    && tofu_base="https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}" \
    && tofu_zip="tofu_${TOFU_VERSION}_linux_${tool_arch}.zip" \
    && curl -fsSLo "/tmp/${tofu_zip}" "${tofu_base}/${tofu_zip}" \
    && curl -fsSLo /tmp/TOFU_SHA256SUMS "${tofu_base}/tofu_${TOFU_VERSION}_SHA256SUMS" \
    && (cd /tmp && grep " ${tofu_zip}$" TOFU_SHA256SUMS | sha256sum -c -) \
    && unzip -q "/tmp/${tofu_zip}" -d /usr/local/bin \
    && tg_base="https://github.com/gruntwork-io/terragrunt/releases/download/v${TERRAGRUNT_VERSION}" \
    && curl -fsSLo "/tmp/terragrunt_linux_${tool_arch}" "${tg_base}/terragrunt_linux_${tool_arch}" \
    && curl -fsSLo /tmp/TERRAGRUNT_SHA256SUMS "${tg_base}/SHA256SUMS" \
    && (cd /tmp && grep " terragrunt_linux_${tool_arch}$" TERRAGRUNT_SHA256SUMS | sha256sum -c -) \
    && install -m 0755 "/tmp/terragrunt_linux_${tool_arch}" /usr/local/bin/terragrunt \
    && curl -fsSLO "https://dl.k8s.io/release/${KUBE_VERSION}/bin/linux/${TARGETARCH:-amd64}/kubectl" \
    && curl -fsSLo kubectl.sha256 "https://dl.k8s.io/release/${KUBE_VERSION}/bin/linux/${TARGETARCH:-amd64}/kubectl.sha256" \
    && echo "$(cat kubectl.sha256)  kubectl" | sha256sum -c - \
    && chmod +x kubectl \
    && mv kubectl /usr/local/bin/ \
    && rm -f kubectl.sha256 /tmp/tofu_* /tmp/TOFU_SHA256SUMS /tmp/terragrunt_linux_* /tmp/TERRAGRUNT_SHA256SUMS \
    && if [ "${BUILDPLATFORM}" = "${TARGETPLATFORM}" ]; then \
        tofu version; \
        terragrunt --version; \
        kubectl version --client; \
        aws --version; \
    fi

CMD ["bash"]
