# self_managed compute mode: EKS Auto Mode is off (compute_config = null in eks.tf).
# Instead the cluster runs a bootstrap managed node group (defined in eks.tf), Cilium as the
# CNI (replacing vpc-cni + kube-proxy), and Karpenter for node provisioning. Every resource in
# this file is gated on eks_compute_mode == "self_managed" so the default "auto" mode is inert.

locals {
  self_managed = var.eks_compute_mode == "self_managed"
}

# Karpenter controller IAM, interruption queue, node IAM role, and Pod Identity association.
# The cluster already uses Pod Identity (see aws_eks_pod_identity_association in eks.tf), so the
# controller authenticates the same way.
module "karpenter" {
  source  = "terraform-aws-modules/eks/aws//modules/karpenter"
  version = "~> 21.0"

  count = local.self_managed ? 1 : 0

  cluster_name = module.eks_cluster.cluster_name

  create_pod_identity_association = true

  node_iam_role_use_name_prefix = false
  node_iam_role_name            = "${local.identifier}-karpenter-node"
  node_iam_role_additional_policies = {
    AmazonSSMManagedInstanceCore = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
  }

  tags = local.tags
}

# Cilium CNI (eBPF dataplane). Replaces both vpc-cni and kube-proxy (kubeProxyReplacement).
# Pods get IPs from an off-VPC overlay (cluster-pool IPAM) tunnelled with VXLAN.
resource "helm_release" "cilium" {
  count = local.self_managed ? 1 : 0

  name       = "cilium"
  repository = "https://helm.cilium.io"
  chart      = "cilium"
  version    = var.cilium_chart_version
  namespace  = "kube-system"

  depends_on = [module.eks_cluster]

  values = [yamlencode({
    kubeProxyReplacement = true
    k8sServiceHost       = replace(module.eks_cluster.cluster_endpoint, "https://", "")
    k8sServicePort       = 443
    ipam = {
      mode = "cluster-pool"
      operator = {
        clusterPoolIPv4PodCIDRList = [var.cilium_pod_cidr]
        clusterPoolIPv4MaskSize    = 24
      }
    }
    routingMode    = "tunnel"
    tunnelProtocol = "vxlan"
    bpf = {
      masquerade = true
    }
    encryption = var.cilium_enable_wireguard ? { enabled = true, type = "wireguard" } : { enabled = false }
    hubble = {
      enabled = true
      relay   = { enabled = true }
      ui      = { enabled = var.cilium_enable_hubble_ui }
    }
    # Tolerate the not-ready/uninitialized taints so Cilium can schedule onto fresh nodes
    # (including the bootstrap node group) before any other CNI is present.
    tolerations = [{ operator = "Exists" }]
  })]
}

# Karpenter controller. Pinned to the bootstrap node group via nodeSelector so it has somewhere
# to run before it provisions any nodes of its own. Installed after Cilium so its pods get IPs.
resource "helm_release" "karpenter" {
  count = local.self_managed ? 1 : 0

  name       = "karpenter"
  repository = "oci://public.ecr.aws/karpenter"
  chart      = "karpenter"
  version    = var.karpenter_chart_version
  namespace  = "kube-system"

  depends_on = [helm_release.cilium, module.karpenter]

  values = [yamlencode({
    settings = {
      clusterName       = module.eks_cluster.cluster_name
      interruptionQueue = module.karpenter[0].queue_name
    }
    serviceAccount = {
      name = module.karpenter[0].service_account
    }
    nodeSelector = {
      "dozuki.com/node-role" = "bootstrap"
    }
  })]
}

# EC2NodeClass: how Karpenter builds nodes (Bottlerocket AMI, node IAM role, subnet/SG discovery
# via the karpenter.sh/discovery tag added to the private subnets and cluster SG).
resource "kubernetes_manifest" "karpenter_node_class" {
  count = local.self_managed ? 1 : 0

  depends_on = [helm_release.karpenter]

  manifest = {
    apiVersion = "karpenter.k8s.aws/v1"
    kind       = "EC2NodeClass"
    metadata = {
      name = "default"
    }
    spec = {
      amiFamily        = "Bottlerocket"
      amiSelectorTerms = [{ alias = "bottlerocket@latest" }]
      role             = module.karpenter[0].node_iam_role_name
      subnetSelectorTerms = [{
        tags = { "karpenter.sh/discovery" = local.identifier }
      }]
      securityGroupSelectorTerms = [{
        tags = { "karpenter.sh/discovery" = local.identifier }
      }]
    }
  }
}

# NodePool: what Karpenter is allowed to provision and when it consolidates.
resource "kubernetes_manifest" "karpenter_node_pool" {
  count = local.self_managed ? 1 : 0

  depends_on = [kubernetes_manifest.karpenter_node_class]

  manifest = {
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata = {
      name = "default"
    }
    spec = {
      template = {
        spec = {
          nodeClassRef = {
            group = "karpenter.k8s.aws"
            kind  = "EC2NodeClass"
            name  = "default"
          }
          requirements = [
            {
              key      = "karpenter.sh/capacity-type"
              operator = "In"
              values   = var.karpenter_node_capacity_types
            },
            {
              key      = "karpenter.k8s.aws/instance-category"
              operator = "In"
              values   = var.karpenter_node_instance_families
            },
          ]
        }
      }
      disruption = {
        consolidationPolicy = "WhenEmptyOrUnderutilized"
        consolidateAfter    = "1m"
      }
    }
  }
}
