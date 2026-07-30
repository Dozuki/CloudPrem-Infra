# OpenTofu, not Terraform: physical requires >= 1.11.1 and logical needs OpenTofu's
# provider for_each, so the old tf-1.5.2 image could not init either layer.
#
# Pinned to the pair the Spacelift fleet runs. Terragrunt 0.99.4 matches exactly, which is
# the version that matters: 1.1.1 double-assumes iam_role and 403s at init on every stack
# that has one (infra-live #158). OpenTofu is 1.11.5 rather than the fleet's 1.11.8 only
# because the base image has no 1.11.8 build.
#
# Distro moves with the tag - this one is Ubuntu, so package installs are apt-get. The
# aws-ot-1.12.x tags are Alpine/apk. Check before changing the FROM.
FROM devopsinfra/docker-terragrunt:aws-ot-1.11.5-tg-0.99.4

ENV KUBE_VERSION="v1.32.0"

# kubectl isn't in the base image; the isolated_run.sh harness needs it. TARGETARCH comes
# from BuildKit, so an arm64 build gets an arm64 kubectl - the arch used to be hardcoded
# to amd64, which silently produced an unrunnable binary when built on an Apple Silicon
# machine. -f so a bad URL fails the build instead of installing an error page as the
# binary, and a --client call so a broken download fails HERE rather than mid-run.
ARG TARGETARCH
RUN set -ex \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -fsSLO "https://dl.k8s.io/release/${KUBE_VERSION}/bin/linux/${TARGETARCH:-amd64}/kubectl" \
    && chmod +x kubectl \
    && mv kubectl /usr/local/bin/ \
    && kubectl version --client \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

CMD ["bash"]
