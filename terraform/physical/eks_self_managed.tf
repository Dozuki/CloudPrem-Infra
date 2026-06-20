# Self-managed dataplane. EKS Auto Mode is off (compute_config unset in eks.tf); instead the
# cluster runs a bootstrap managed node group (defined in eks.tf), Cilium as the CNI (replacing
# vpc-cni + kube-proxy), Karpenter for node provisioning, the AWS Load Balancer Controller (for
# the NLB TargetGroupBinding the logical layer uses), and the EBS CSI driver (for ebs-gp3).

# These resources were previously count-gated on eks_compute_mode == "self_managed". The toggle
# is gone (self-managed is the only mode), so the moved blocks below re-home the already-applied
# [0]-indexed instances to their un-indexed addresses without destroy/recreate.
moved {
  from = module.karpenter[0]
  to   = module.karpenter
}
moved {
  from = null_resource.cilium_bootstrap[0]
  to   = null_resource.cilium_bootstrap
}
moved {
  from = null_resource.karpenter_bootstrap[0]
  to   = null_resource.karpenter_bootstrap
}
moved {
  from = aws_iam_policy.aws_lb_controller[0]
  to   = aws_iam_policy.aws_lb_controller
}
moved {
  from = aws_iam_role.aws_lb_controller[0]
  to   = aws_iam_role.aws_lb_controller
}
moved {
  from = aws_iam_role_policy_attachment.aws_lb_controller[0]
  to   = aws_iam_role_policy_attachment.aws_lb_controller
}
moved {
  from = aws_eks_pod_identity_association.aws_lb_controller[0]
  to   = aws_eks_pod_identity_association.aws_lb_controller
}
moved {
  from = null_resource.aws_lb_controller_bootstrap[0]
  to   = null_resource.aws_lb_controller_bootstrap
}

# Karpenter controller IAM, interruption queue, node IAM role, and Pod Identity association.
# The cluster uses Pod Identity (see eks-pod-identity-agent addon), so the controller
# authenticates the same way.
module "karpenter" {
  source  = "terraform-aws-modules/eks/aws//modules/karpenter"
  version = "~> 21.0"

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
  depends_on = [module.eks_cluster, module.karpenter, null_resource.cilium_bootstrap]

  triggers = {
    version = var.karpenter_chart_version
    queue   = module.karpenter.queue_name
    cluster = local.identifier
  }

  provisioner "local-exec" {
    interpreter = ["bash", "-c"]
    command = templatefile("${path.module}/scripts/karpenter-bootstrap.sh.tftpl", {
      cluster_name    = local.identifier
      region          = data.aws_region.current.id
      chart_version   = var.karpenter_chart_version
      queue_name      = module.karpenter.queue_name
      service_account = module.karpenter.service_account
    })
  }
}

# The Karpenter NodePool + EC2NodeClass custom resources live in the logical layer
# (terraform/logical/karpenter.tf), where the cluster is guaranteed live and the
# Karpenter CRDs installed by the controller above already exist.

# --- AWS Load Balancer Controller ------------------------------------------------
# Provides the TargetGroupBinding CRD + controller the logical layer uses to bind the
# physical NLB target groups to the Envoy Gateway pods. Installs via IAM + Pod Identity +
# helm (local-exec), like Karpenter.
resource "aws_iam_policy" "aws_lb_controller" {
  name   = "${local.identifier}-aws-lb-controller"
  policy = file("${path.module}/static/aws-lb-controller-iam-policy.json")
  tags   = local.tags
}

resource "aws_iam_role" "aws_lb_controller" {
  name = "${local.identifier}-aws-lb-controller"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "pods.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "aws_lb_controller" {
  role       = aws_iam_role.aws_lb_controller.name
  policy_arn = aws_iam_policy.aws_lb_controller.arn
}

resource "aws_eks_pod_identity_association" "aws_lb_controller" {
  cluster_name    = module.eks_cluster.cluster_name
  namespace       = "kube-system"
  service_account = "aws-load-balancer-controller"
  role_arn        = aws_iam_role.aws_lb_controller.arn
}

resource "null_resource" "aws_lb_controller_bootstrap" {
  depends_on = [
    module.eks_cluster,
    aws_eks_pod_identity_association.aws_lb_controller,
    null_resource.cilium_bootstrap,
  ]

  triggers = {
    version = var.aws_lb_controller_chart_version
    cluster = local.identifier
  }

  provisioner "local-exec" {
    interpreter = ["bash", "-c"]
    command = templatefile("${path.module}/scripts/aws-lb-controller-bootstrap.sh.tftpl", {
      cluster_name  = local.identifier
      region        = data.aws_region.current.id
      vpc_id        = local.vpc_id
      chart_version = var.aws_lb_controller_chart_version
    })
  }
}

# --- EBS CSI driver IAM -----------------------------------------------------------
# The aws-ebs-csi-driver managed addon (eks.tf) provisions the ebs-gp3 StorageClass volumes.
# Its controller (ServiceAccount ebs-csi-controller-sa in kube-system) needs EC2 volume
# permissions, granted here via Pod Identity + the AWS-managed AmazonEBSCSIDriverPolicy.
resource "aws_iam_role" "ebs_csi" {
  name = "${local.identifier}-ebs-csi"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "pods.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "ebs_csi" {
  role       = aws_iam_role.ebs_csi.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
}

resource "aws_eks_pod_identity_association" "ebs_csi" {
  cluster_name    = module.eks_cluster.cluster_name
  namespace       = "kube-system"
  service_account = "ebs-csi-controller-sa"
  role_arn        = aws_iam_role.ebs_csi.arn
}
