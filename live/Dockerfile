FROM devopsinfra/docker-terragrunt:aws-tf-1.5.2-tg-0.48.1

ENV KUBE_VERSION="v1.32.0"

# kubectl isn't in the base image; the isolated_run.sh harness needs it.
RUN set -ex \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -LO https://dl.k8s.io/release/${KUBE_VERSION}/bin/linux/amd64/kubectl \
    && chmod +x kubectl \
    && mv kubectl /usr/local/bin/ \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

CMD ["bash"]
