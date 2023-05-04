# Use the specified base image
FROM devopsinfra/docker-terragrunt:aws-tf-1.3.9-tg-0.44.0

# Set environment variables for kubectl version
ENV KUBE_VERSION="v1.23.0"

# Install kubectl and required dependencies
RUN set -ex \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -LO https://storage.googleapis.com/kubernetes-release/release/${KUBE_VERSION}/bin/linux/amd64/kubectl \
    && chmod +x kubectl \
    && mv kubectl /usr/local/bin/ \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Set the default command
CMD ["bash"]
