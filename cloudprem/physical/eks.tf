resource "aws_iam_policy" "cluster_autoscaler_policy" {
  name_prefix = "cluster-autoscaler"
  description = "EKS cluster-autoscaler policy for cluster ${module.eks_cluster.cluster_id}"
  policy      = data.aws_iam_policy_document.cluster_autoscaler_pd.json
}

#tfsec:ignore:aws-iam-no-policy-wildcards
data "aws_iam_policy_document" "cluster_autoscaler_pd" {
  statement {
    sid    = "clusterAutoscalerAll"
    effect = "Allow"

    actions = [
      "autoscaling:DescribeAutoScalingGroups",
      "autoscaling:DescribeAutoScalingInstances",
      "autoscaling:DescribeLaunchConfigurations",
      "autoscaling:DescribeTags",
      "ec2:DescribeLaunchTemplateVersions",
    ]

    resources = ["*"]
  }

  statement {
    sid    = "clusterAutoscalerOwn"
    effect = "Allow"

    actions = [
      "autoscaling:SetDesiredCapacity",
      "autoscaling:TerminateInstanceInAutoScalingGroup",
      "autoscaling:UpdateAutoScalingGroup",
    ]

    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "autoscaling:ResourceTag/k8s.io/cluster-autoscaler/${module.eks_cluster.cluster_id}"
      values   = ["owned"]
    }

    condition {
      test     = "StringEquals"
      variable = "autoscaling:ResourceTag/k8s.io/cluster-autoscaler/enabled"
      values   = ["true"]
    }
  }
}

# IAM role that the bastion host can assume.
module "cluster_access_role_assumable" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role"
  version = "5.2.0"

  create_role = true

  role_name         = "${local.identifier}-${data.aws_region.current.name}-cluster-access-assumable"
  role_requires_mfa = false

  custom_role_policy_arns = [
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/ReadOnlyAccess",
    aws_iam_policy.cluster_access.arn,
  ]

  trusted_role_arns = [
    "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root",
  ]

  tags = local.tags
}

# IAM role to access the EKS cluster. By default only the user that creates the cluster has access to it
module "cluster_access_role" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc"
  version = "5.2.0"

  create_role = true
  role_name   = local.cluster_access_role_name

  provider_url = replace(module.eks_cluster.cluster_oidc_issuer_url, "https://", "")
  role_policy_arns = [
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/ReadOnlyAccess",
    aws_iam_policy.cluster_access.arn,
    aws_iam_policy.cluster_autoscaler_policy.arn
  ]
  oidc_fully_qualified_subjects = [
    "system:serviceaccount:kube-system:cluster-autoscaler-aws-cluster-autoscaler-chart"
  ]

  tags = local.tags
}

data "aws_iam_policy_document" "cluster_access" {
  statement {
    actions = [
      "eks:AccessKubernetesApi",
    ]

    resources = [
      "arn:${data.aws_partition.current.partition}:eks:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:cluster/${local.identifier}",
    ]
  }
}

resource "aws_iam_policy" "cluster_access" {
  name   = "${local.identifier}-${data.aws_region.current.name}-cluster-access"
  policy = data.aws_iam_policy_document.cluster_access.json
}

#tfsec:ignore:aws-iam-no-policy-wildcards
data "aws_iam_policy_document" "eks_worker" {
  statement {
    actions = [
      "s3:PutObject",
      "s3:PutObjectAcl",
      "s3:GetObject",
      "s3:GetObjectAcl",
      "s3:ListBucket",
      "s3:ListObjectsV2",
      "s3:CopyObject"
    ]

    resources = [
      "arn:${data.aws_partition.current.partition}:s3:::*",
    ]
  }

  statement {
    actions = [
      "kms:Encrypt",
      "kms:Decrypt",
      "kms:ReEncryptTo",
      "kms:ReEncryptFrom",
      "kms:GenerateDataKey",
      "kms:GenerateDataKeyPair",
      "kms:GenerateDataKeyPairWithoutPlaintext",
      "kms:GenerateDataKeyWithoutPlaintext",
      "kms:DescribeKey",
    ]

    resources = [
      data.aws_kms_key.s3.arn,
    ]
  }

  statement {
    actions = [
      "rds:CreateDBSnapshot",
      "rds:DescribeDBSnapshots"
    ]

    resources = ["*"]
  }

  statement {
    actions = [
      "logs:*",
    ]

    resources = ["*"]
  }

  statement {
    actions = [
      "dms:StartReplicationTask"
    ]

    resources = ["*"]
  }
}

# This policy is required for the KMS key used for EKS root volumes, so the cluster is allowed to enc/dec/attach encrypted EBS volumes
data "aws_iam_policy_document" "ebs" {
  # Copy of default KMS policy that lets you manage it
  statement {
    sid       = "Enable IAM User Permissions"
    actions   = ["kms:*"]
    resources = ["*"]

    principals {
      type        = "AWS"
      identifiers = ["arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
  }

  # Required for EKS
  statement {
    sid = "Allow service-linked role use of the CMK"
    actions = [
      "kms:Encrypt",
      "kms:Decrypt",
      "kms:ReEncrypt*",
      "kms:GenerateDataKey*",
      "kms:DescribeKey"
    ]
    resources = ["*"]

    principals {
      type = "AWS"
      identifiers = [
        "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:role/aws-service-role/autoscaling.amazonaws.com/AWSServiceRoleForAutoScaling", # required for the ASG to manage encrypted volumes for nodes
        module.eks_cluster.cluster_iam_role_arn,                                                                                                                                        # required for the cluster / persistentvolume-controller to create encrypted PVCs
      ]
    }
  }

  statement {
    sid       = "Allow attachment of persistent resources"
    actions   = ["kms:CreateGrant"]
    resources = ["*"]

    principals {
      type = "AWS"
      identifiers = [
        "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:role/aws-service-role/autoscaling.amazonaws.com/AWSServiceRoleForAutoScaling", # required for the ASG to manage encrypted volumes for nodes
        module.eks_cluster.cluster_iam_role_arn,                                                                                                                                        # required for the cluster / persistentvolume-controller to create encrypted PVCs
      ]
    }

    condition {
      test     = "Bool"
      variable = "kms:GrantIsForAWSResource"
      values   = ["true"]
    }
  }
}

resource "aws_kms_key" "ebs" {
  description             = "Customer managed key to encrypt self managed node group volumes"
  deletion_window_in_days = 7
  policy                  = data.aws_iam_policy_document.ebs.json
}

resource "aws_iam_policy" "eks_worker" {
  name   = "${local.identifier}-${data.aws_region.current.name}"
  policy = data.aws_iam_policy_document.eks_worker.json
}

resource "aws_kms_key" "eks" {
  count = local.create_eks_kms ? 1 : 0

  description         = "EKS Secret Encryption Key"
  enable_key_rotation = true
}

module "eks_cluster" {
  source  = "terraform-aws-modules/eks/aws"
  version = "18.27.1"

  cluster_name = local.identifier
  #  cluster_version = "1.21"
  enable_irsa                     = true
  cluster_endpoint_private_access = true
  cluster_endpoint_public_access  = true
  iam_role_arn                    = aws_iam_policy.eks_worker.arn

  cluster_addons = {
    coredns = {
      resolve_conflicts = "OVERWRITE"
    }
    kube-proxy = {}
    vpc-cni = {
      resolve_conflicts = "OVERWRITE"
    }
  }

  cluster_encryption_config = [{
    provider_key_arn = local.eks_kms_key
    resources        = ["secrets"]
  }]

  vpc_id     = local.vpc_id
  subnet_ids = local.private_subnet_ids

  eks_managed_node_groups = {
    workers = {
      min_size     = var.eks_min_size
      max_size     = var.eks_max_size
      desired_size = var.eks_desired_capacity

      instance_types = var.eks_instance_types
      capacity_type  = "SPOT"
      labels         = local.tags

      taints = {
        dedicated = {
          key    = "dedicated"
          value  = "gpuGroup"
          effect = "NO_SCHEDULE"
        }
      }

      update_config = {
        max_unavailable_percentage = 50 # or set `max_unavailable`
      }
      tags = {
        "aws-node-termination-handler/managed" = true
        "k8s.io/cluster-autoscaler/enabled"    = true
        "k8s.io/cluster-autoscaler/${local.identifier}" : "owned"
      }
    }
  }

  manage_aws_auth_configmap = true

  aws_auth_roles = [ # aws-auth configmap
    {
      rolearn  = module.cluster_access_role.iam_role_arn
      username = "admin"
      groups   = ["system:masters"]
    },
    {
      rolearn  = module.cluster_access_role_assumable.iam_role_arn
      username = "admin"
      groups   = ["system:masters"]
    },
  ]

  #  bootstrap_extra_args = "--kubelet-extra-args '--node-labels=node.kubernetes.io/lifecycle=spot'"
  #
  #  use_mixed_instances_policy = true
  #  mixed_instances_policy = {
  #    instances_distribution = {
  #      on_demand_base_capacity                  = 0
  #      on_demand_percentage_above_base_capacity = 20
  #      spot_allocation_strategy                 = "capacity-optimized"
  #    }
  #
  #    override = [
  #      {
  #        instance_type     = "m5.large"
  #        weighted_capacity = "1"
  #      },
  #      {
  #        instance_type     = "m6i.large"
  #        weighted_capacity = "2"
  #      },
  #    ]
  #  }

  # Extend cluster security group rules
  cluster_security_group_additional_rules = {
    ingress_to_nodes_from_nlb_http = {
      description                = "App Access to Nodes"
      protocol                   = "tcp"
      from_port                  = 32010
      to_port                    = 32010
      type                       = "ingress"
      source_node_security_group = true
    }
    ingress_to_nodes_from_nlb_https = {
      description                = "https App Access to Nodes"
      protocol                   = "tcp"
      from_port                  = 32005
      to_port                    = 32005
      type                       = "ingress"
      source_node_security_group = true
    }
    ingress_to_nodes_from_nlb_replicated = {
      description                = "App Access to Nodes"
      protocol                   = "tcp"
      from_port                  = 32001
      to_port                    = 32001
      type                       = "ingress"
      source_node_security_group = true
    }
  }

  #  self_managed_node_group_defaults = {
  #
  #    # enable discovery of autoscaling groups by cluster-autoscaler
  #    autoscaling_group_tags = {
  #      "k8s.io/cluster-autoscaler/enabled" : true,
  #      "k8s.io/cluster-autoscaler/${local.identifier}" : "owned",
  #      "aws-node-termination-handler/managed": true,
  #      "Environment": var.environment
  #    }
  #  }
}




#tfsec:ignore:aws-vpc-no-public-egress-sgr
#tfsec:ignore:aws-eks-no-public-cluster-access-to-cidr
#tfsec:ignore:aws-eks-no-public-cluster-access
#tfsec:ignore:aws-eks-encrypt-secrets
#tfsec:ignore:aws-eks-enable-control-plane-logging
#module "eks_cluster" {
#  source  = "terraform-aws-modules/eks/aws"
#  version = "17.24.0"
#
#  depends_on = [aws_iam_policy.cluster_access, aws_iam_policy.eks_worker]
#
#  # EKS cofigurations
#  cluster_name    = local.identifier
#  cluster_version = "1.21"
#  enable_irsa     = true
#  # Need public access even when deploying from AWS due to the occasional inability to access private endpoints.
#  cluster_endpoint_public_access = true
#  #  cluster_endpoint_private_access                = true
#  #  cluster_endpoint_private_access_cidrs          = [local.vpc_cidr]
#  #  cluster_create_endpoint_private_access_sg_rule = true
#
#  cluster_encryption_config = [
#    {
#      provider_key_arn = local.eks_kms_key
#      resources        = ["secrets"]
#    }
#  ]
#
#  vpc_id  = local.vpc_id
#  subnets = local.private_subnet_ids
#
#  workers_additional_policies = [
#    aws_iam_policy.eks_worker.arn,
#  ]
#  worker_groups_launch_template = [
#    {
#      name                                 = "workers"
#      asg_max_size                         = var.eks_max_size
#      asg_desired_capacity                 = var.eks_desired_capacity
#      instance_refresh_enabled             = true
#      instance_refresh_instance_warmup     = 60
#      public_ip                            = false
#      metadata_http_put_response_hop_limit = 3
#      spot_instance_pools                  = 4
#      update_default_version               = true
#      instance_refresh_triggers            = ["tag"]
#      kubelet_extra_args                   = "--node-labels=node.kubernetes.io/lifecycle=spot"
#      instance_type                        = "m5.large"
#      override_instance_types              = var.eks_instance_types
#      target_group_arns                    = module.nlb.target_group_arns
#      tags = [
#        {
#          key                 = "aws-node-termination-handler/managed"
#          value               = ""
#          propagate_at_launch = true
#        },
#        {
#          key                 = "Environment"
#          value               = var.environment
#          propagate_at_launch = true
#        },
#        {
#          key                 = "k8s.io/cluster-autoscaler/enabled"
#          value               = true
#          propagate_at_launch = true
#        },
#        {
#          key                 = "k8s.io/cluster-autoscaler/${local.identifier}"
#          value               = "owned"
#          propagate_at_launch = true
#        }
#      ]
#    }
#  ]
#
#  # Kubernetes configurations
#  write_kubeconfig = false
#  # Give both roles admin access due to the need for the OIDC assumable role and the basic assumable role. The bastion
#  # host does not seem to support the OIDC role at all so a second one was required.
#  map_roles = [ # aws-auth configmap
#    {
#      rolearn  = module.cluster_access_role.iam_role_arn
#      username = "admin"
#      groups   = ["system:masters"]
#    },
#    {
#      rolearn  = module.cluster_access_role_assumable.iam_role_arn
#      username = "admin"
#      groups   = ["system:masters"]
#    },
#  ]
#
#  tags = local.tags
#}