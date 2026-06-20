# Karpenter NodePool + EC2NodeClass. These are Kubernetes custom resources, so
# they live in the logical layer where the cluster is guaranteed live and the
# Karpenter CRDs (installed by the controller in the physical layer) already exist.
locals {
  karpenter_enabled = var.cloud == "aws"
}

resource "kubernetes_manifest" "karpenter_node_class" {
  count = local.karpenter_enabled ? 1 : 0
  manifest = {
    apiVersion = "karpenter.k8s.aws/v1"
    kind       = "EC2NodeClass"
    metadata   = { name = "default" }
    spec = {
      amiFamily                  = "Bottlerocket"
      amiSelectorTerms           = [{ alias = "bottlerocket@latest" }]
      role                       = var.karpenter_node_iam_role_name
      subnetSelectorTerms        = [{ tags = { "karpenter.sh/discovery" = var.eks_cluster_id } }]
      securityGroupSelectorTerms = [{ tags = { "karpenter.sh/discovery" = var.eks_cluster_id } }]
    }
  }
}

resource "kubernetes_manifest" "karpenter_node_pool" {
  count      = local.karpenter_enabled ? 1 : 0
  depends_on = [kubernetes_manifest.karpenter_node_class]

  # Terraform owns this manifest via server-side apply; force past field-manager
  # conflicts from any ad-hoc kubectl edits to the NodePool.
  field_manager {
    force_conflicts = true
  }

  manifest = {
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata   = { name = "default" }
    spec = {
      template = {
        spec = {
          nodeClassRef = { group = "karpenter.k8s.aws", kind = "EC2NodeClass", name = "default" }
          requirements = [
            { key = "karpenter.sh/capacity-type", operator = "In", values = var.karpenter_node_capacity_types },
            { key = "karpenter.k8s.aws/instance-category", operator = "In", values = var.karpenter_node_instance_families },
            # The Dozuki app images are amd64-only; without this Karpenter also
            # provisions Graviton (arm64) nodes and pods fail with "exec format error".
            { key = "kubernetes.io/arch", operator = "In", values = ["amd64"] },
          ]
        }
      }
      disruption = { consolidationPolicy = "WhenEmptyOrUnderutilized", consolidateAfter = "1m" }
    }
  }
}
