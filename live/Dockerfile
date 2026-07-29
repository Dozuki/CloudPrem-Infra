# OpenTofu, not Terraform: physical requires >= 1.11.1 and logical needs OpenTofu's
# provider for_each, so the old tf-1.5.2 image could not init either layer. Terragrunt
# 1.1.1 matches what the Spacelift fleet pins; OpenTofu is 1.12.4 rather than the fleet's
# 1.12.5 only because the base image has no 1.12.5 build yet.
#
# This image is Alpine (the old tf-1.5.2 tag was Debian), so package installs are apk.
FROM devopsinfra/docker-terragrunt:aws-ot-1.12.4-tg-1.1.1

ENV KUBE_VERSION="v1.32.0"

# kubectl isn't in the base image; the isolated_run.sh harness needs it. TARGETARCH comes
# from BuildKit, so an arm64 build gets an arm64 kubectl - the arch used to be hardcoded
# to amd64, which silently produced an unrunnable binary when built on an Apple Silicon
# machine. -f so a bad URL fails the build instead of installing an error page as the
# binary, and a --client call so a broken download fails HERE rather than mid-run.
ARG TARGETARCH
RUN set -ex \
    && apk add --no-cache ca-certificates curl \
    && curl -fsSLO "https://dl.k8s.io/release/${KUBE_VERSION}/bin/linux/${TARGETARCH:-amd64}/kubectl" \
    && chmod +x kubectl \
    && mv kubectl /usr/local/bin/ \
    && kubectl version --client

CMD ["bash"]
