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
#
# Installed imperatively (helm via local-exec) rather than via the Terraform helm provider:
# the provider would have to be configured from this same apply's cluster outputs, which
# forces a -target on first create and deadlocks with the in-module bootstrap node group
# (the node group waits for nodes to be Ready, but nodes can't be Ready until a CNI exists).
# This null_resource intentionally has NO depends_on so it runs CONCURRENTLY with the module:
# the script polls for the cluster by name, then installs Cilium, making the bootstrap nodes
# Ready before the node group's create timeout.
resource "null_resource" "cilium_bootstrap" {
  count = local.self_managed ? 1 : 0

  triggers = {
    version   = var.cilium_chart_version
    pod_cidr  = var.cilium_pod_cidr
    hubble_ui = tostring(var.cilium_enable_hubble_ui)
    wireguard = tostring(var.cilium_enable_wireguard)
    cluster   = local.identifier
  }

  provisioner "local-exec" {
    interpreter = ["bash", "-c"]
    command = templatefile("${path.module}/scripts/cilium-bootstrap.sh.tftpl", {
      cluster_name  = local.identifier
      region        = data.aws_region.current.id
      chart_version = var.cilium_chart_version
      pod_cidr      = var.cilium_pod_cidr
      hubble_ui     = tostring(var.cilium_enable_hubble_ui)
      wireguard     = tostring(var.cilium_enable_wireguard)
    })
  }
}

# Karpenter controller. Pinned to the bootstrap node group via nodeSelector so it has somewhere
# to run before it provisions any nodes of its own. Installed (imperative helm via local-exec)
# after the cluster, the Karpenter IAM/queue module, and Cilium are all up so its pods get IPs.
resource "null_resource" "karpenter_bootstrap" {
  count = local.self_managed ? 1 : 0

  depends_on = [module.eks_cluster, module.karpenter, null_resource.cilium_bootstrap]

  triggers = {
    version = var.karpenter_chart_version
    queue   = module.karpenter[0].queue_name
    cluster = local.identifier
  }

  provisioner "local-exec" {
    interpreter = ["bash", "-c"]
    command = templatefile("${path.module}/scripts/karpenter-bootstrap.sh.tftpl", {
      cluster_name    = local.identifier
      region          = data.aws_region.current.id
      chart_version   = var.karpenter_chart_version
      queue_name      = module.karpenter[0].queue_name
      service_account = module.karpenter[0].service_account
    })
  }
}

# The Karpenter NodePool + EC2NodeClass custom resources live in the logical layer
# (terraform/logical/karpenter.tf), where the cluster is guaranteed live and the
# Karpenter CRDs installed by the controller above already exist.
