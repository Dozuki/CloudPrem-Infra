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
