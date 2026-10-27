# EKS Auto Mode ships its NetworkPolicy agent idle. It only enforces once this
# ConfigMap says so; without it every NetworkPolicy in the cluster is accepted
# by the API server and silently does nothing. Unconditional on AWS by design:
# there is no supported state where policies exist but are not enforced.
resource "kubernetes_config_map_v1" "amazon_vpc_cni" {
  count = var.cloud == "aws" ? 1 : 0

  metadata {
    name      = "amazon-vpc-cni"
    namespace = "kube-system"
    labels    = { "app.kubernetes.io/managed-by" = "terraform" }
  }

  data = { "enable-network-policy-controller" = "true" }
}
