# syntax=docker/dockerfile:1
#
# Runtime image for the CloudPrem test harness phases (provision/upgrade/validate/
# teardown). One image, one binary, four subcommands - the same code path run.sh drives
# locally, so a container run and a laptop run stay comparable.
#
# Build from the REPO ROOT (the provider warm stage needs terraform/):
#   docker buildx build --platform linux/amd64 -f live/tests/Dockerfile -t <tag> .
#
# Always --platform linux/amd64. A plain build on an arm64 Mac stamps arm64 and emits an
# OCI index that EKS cannot pull; `docker inspect .Architecture` reports the host arch and
# is NOT proof. Check the registry manifest instead.
#
# The single biggest runtime cost this image removes is `tofu init`. The harness runs
# terragrunt once per layer per git worktree, so an upgrade scenario pays init 4+ times,
# each pulling ~12 providers (aws and azurerm alone are most of a gigabyte). Stage 3 warms
# a plugin cache from the repo's committed lockfiles so those inits resolve locally.

ARG ALPINE_VERSION=3.22
ARG GO_VERSION=1.25

# Fleet pins. tofu/terragrunt MUST match infra-live/_spacelift/stacks.tf: the pair moves
# together (terragrunt 0.99.4 caps tofu at 1.11.x) and terragrunt 1.x double-assumes
# iam_role. run.sh warns when the local terragrunt is off this pin; the image cannot drift.
ARG TOFU_VERSION=1.11.8
ARG TERRAGRUNT_VERSION=0.99.4
# Matched to the toolchain the harness is known green on locally, so the container is a
# fair comparison rather than a second variable.
ARG HELM_VERSION=4.2.3
ARG KUBECTL_VERSION=1.36.3
ARG VAULT_VERSION=2.0.3

# ---------------------------------------------------------------------------
# Stage 1: tools. Every download is checksum-verified against the publisher's
# SHA256SUMS - this image carries admin credentials for a whole AWS account.
# ---------------------------------------------------------------------------
FROM alpine:${ALPINE_VERSION} AS tools
ARG TOFU_VERSION
ARG TERRAGRUNT_VERSION
ARG HELM_VERSION
ARG KUBECTL_VERSION
ARG VAULT_VERSION
RUN apk add --no-cache curl unzip tar
WORKDIR /out

RUN set -eu; \
    curl -fsSLO "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_amd64.zip"; \
    curl -fsSLO "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_SHA256SUMS"; \
    grep " tofu_${TOFU_VERSION}_linux_amd64.zip\$" "tofu_${TOFU_VERSION}_SHA256SUMS" | sha256sum -c -; \
    unzip -q "tofu_${TOFU_VERSION}_linux_amd64.zip" tofu; \
    chmod +x tofu

RUN set -eu; \
    curl -fsSL -o terragrunt "https://github.com/gruntwork-io/terragrunt/releases/download/v${TERRAGRUNT_VERSION}/terragrunt_linux_amd64"; \
    curl -fsSL -o tg_SHA256SUMS "https://github.com/gruntwork-io/terragrunt/releases/download/v${TERRAGRUNT_VERSION}/SHA256SUMS"; \
    want="$(grep " terragrunt_linux_amd64\$" tg_SHA256SUMS | awk '{print $1}')"; \
    echo "${want}  terragrunt" | sha256sum -c -; \
    chmod +x terragrunt

RUN set -eu; \
    curl -fsSLO "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz"; \
    want="$(curl -fsSL "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz.sha256sum" | awk '{print $1}')"; \
    echo "${want}  helm-v${HELM_VERSION}-linux-amd64.tar.gz" | sha256sum -c -; \
    tar -xzf "helm-v${HELM_VERSION}-linux-amd64.tar.gz" --strip-components=1 linux-amd64/helm; \
    chmod +x helm

RUN set -eu; \
    curl -fsSLO "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/amd64/kubectl"; \
    want="$(curl -fsSL "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/amd64/kubectl.sha256")"; \
    echo "${want}  kubectl" | sha256sum -c -; \
    chmod +x kubectl

RUN set -eu; \
    curl -fsSLO "https://releases.hashicorp.com/vault/${VAULT_VERSION}/vault_${VAULT_VERSION}_linux_amd64.zip"; \
    curl -fsSL -o vault_SHA256SUMS "https://releases.hashicorp.com/vault/${VAULT_VERSION}/vault_${VAULT_VERSION}_SHA256SUMS"; \
    grep " vault_${VAULT_VERSION}_linux_amd64.zip\$" vault_SHA256SUMS | sha256sum -c -; \
    unzip -q "vault_${VAULT_VERSION}_linux_amd64.zip" vault; \
    chmod +x vault

RUN rm -f /out/*.zip /out/*.tar.gz /out/*SHA256SUMS /out/*.sha256

# ---------------------------------------------------------------------------
# Stage 2: build the harness binaries. Cache mounts keep rebuilds to the compile
# itself; only live/tests is copied, so a terraform/ edit does not invalidate it.
# ---------------------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS gobuild
WORKDIR /src
COPY live/tests/go.mod live/tests/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY live/tests/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/harness ./cmd/harness && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/cpi-dr ./cmd/cpi-dr

# ---------------------------------------------------------------------------
# Stage 3: warm the provider plugin cache.
#
# `tofu init -backend=false` resolves providers from each layer's committed lockfile with
# no AWS credentials, so this is buildable in CI. Both layers are warmed; azurerm is
# included deliberately - it runs with zero instances on AWS but init still downloads it.
#
# Best-effort by design. The cache is keyed by provider version, and the harness tests
# OLD refs too (an upgrade run checks out a baseline tag whose lockfile may pin different
# versions). A miss falls back to a normal download, so a stale cache costs time, never
# correctness.
# ---------------------------------------------------------------------------
FROM alpine:${ALPINE_VERSION} AS providers
COPY --from=tools /out/tofu /usr/local/bin/tofu
ENV TF_PLUGIN_CACHE_DIR=/opt/tofu-plugin-cache \
    TF_IN_AUTOMATION=1 \
    CHECKPOINT_DISABLE=1
RUN mkdir -p "${TF_PLUGIN_CACHE_DIR}"
COPY terraform/ /warm/terraform/
RUN set -eu; \
    for layer in physical logical; do \
      echo ">> warming providers for terraform/${layer}"; \
      ( cd "/warm/terraform/${layer}" && tofu init -backend=false -input=false ) \
        || echo ">> WARNING: warm init failed for ${layer}; runtime will download instead"; \
    done; \
    rm -rf /warm; \
    du -sh "${TF_PLUGIN_CACHE_DIR}"

# ---------------------------------------------------------------------------
# Stage 4: runtime
# ---------------------------------------------------------------------------
FROM alpine:${ALPINE_VERSION}

# bash: generate_live_env.sh and the harness shell helpers are bash, not sh.
# git: the harness builds a git worktree per ref (dual-ref upgrades).
# openssl: self-signed cert for the manual-TLS path (see the entrypoint).
# aws-cli: 4 call sites remain in Go (eks update-kubeconfig, elbv2 x2, export-credentials).
#   Alpine's community build is musl-native, which is why this image is not Debian.
# jq: the cleanup/verify shell backstops.
RUN apk add --no-cache \
        bash \
        ca-certificates \
        git \
        openssl \
        curl \
        jq \
        aws-cli \
    && rm -rf /var/cache/apk/*

COPY --from=tools /out/tofu /out/terragrunt /out/helm /out/kubectl /out/vault /usr/local/bin/
COPY --from=gobuild /out/harness /out/cpi-dr /usr/local/bin/
COPY --from=providers /opt/tofu-plugin-cache /opt/tofu-plugin-cache

# The az-env-gap guard, baked in rather than mktemp'd per run. Spacelift's workers have no
# usable Azure CLI; shimming `az` to fail here is what makes the azurerm-on-AWS class of
# bug surface in a test run instead of at real deploy time. The entrypoint drops it from
# PATH when NO_AZ_SHIM=1.
RUN mkdir -p /opt/az-shim && \
    printf '%s\n' \
      '#!/usr/bin/env bash' \
      "echo \">> [harness] 'az' is intentionally shimmed to fail - simulating Spacelift workers (no Azure CLI). Set NO_AZ_SHIM=1 to disable.\" >&2" \
      'exit 1' > /opt/az-shim/az && \
    chmod +x /opt/az-shim/az

# TG_TF_PATH: terragrunt must drive OpenTofu (the physical layer needs master_password_wo).
# TF_PLUGIN_CACHE_DIR: the warm cache from stage 3. The harness passes the ambient
#   environment through to terragrunt (append(os.Environ(), ...)), so this is all it takes.
# CHECKPOINT_DISABLE / TF_IN_AUTOMATION: drop the per-init phone-home and interactive
#   affordances. Small, but they are pure latency on a path run 4+ times per run.
ENV PATH="/opt/az-shim:/usr/local/bin:${PATH}" \
    TG_TF_PATH=tofu \
    TF_PLUGIN_CACHE_DIR=/opt/tofu-plugin-cache \
    TF_IN_AUTOMATION=1 \
    TF_INPUT=0 \
    CHECKPOINT_DISABLE=1 \
    RUN_INTEGRATION=1 \
    HARNESS_REPO_DIR=/workspace/repo \
    HARNESS_REPO_URL=https://github.com/Dozuki/CloudPrem-Infra.git

COPY live/tests/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["--help"]
