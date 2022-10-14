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
  version = "4.7.0"

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
  version = "4.7.0"

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

resource "aws_iam_policy" "eks_worker" {
  name   = "${local.identifier}-${data.aws_region.current.name}"
  policy = data.aws_iam_policy_document.eks_worker.json
}

resource "aws_kms_key" "eks" {
  count = local.create_eks_kms ? 1 : 0

  description         = "EKS Secret Encryption Key"
  enable_key_rotation = true
}

#tfsec:ignore:aws-vpc-no-public-egress-sgr
#tfsec:ignore:aws-eks-no-public-cluster-access-to-cidr
#tfsec:ignore:aws-eks-no-public-cluster-access
#tfsec:ignore:aws-eks-encrypt-secrets
#tfsec:ignore:aws-eks-enable-control-plane-logging
module "eks_cluster" {
  source  = "terraform-aws-modules/eks/aws"
  version = "17.24.0"

  depends_on = [aws_iam_policy.cluster_access, aws_iam_policy.eks_worker]

  # EKS cofigurations
  cluster_name    = local.identifier
  cluster_version = "1.21"
  enable_irsa     = true
  # Need public access even when deploying from AWS due to the occasional inability to access private endpoints.
  cluster_endpoint_public_access                 = true
  cluster_endpoint_private_access                = true
  cluster_endpoint_private_access_cidrs          = [local.vpc_cidr]
  cluster_create_endpoint_private_access_sg_rule = true

  cluster_encryption_config = [
    {
      provider_key_arn = local.eks_kms_key
      resources        = ["secrets"]
    }
  ]

  vpc_id  = local.vpc_id
  subnets = local.private_subnet_ids

  workers_additional_policies = [
    aws_iam_policy.eks_worker.arn,
  ]
  worker_groups_launch_template = [
    {
      name                                 = "workers"
      asg_max_size                         = var.eks_max_size
      asg_desired_capacity                 = var.eks_desired_capacity
      asg_min_size                         = var.eks_min_size
      instance_refresh_enabled             = true
      instance_refresh_instance_warmup     = 60
      public_ip                            = false
      metadata_http_put_response_hop_limit = 3
      spot_instance_pools                  = 4
      update_default_version               = true
      instance_refresh_triggers            = ["tag"]
      kubelet_extra_args                   = "--node-labels=node.kubernetes.io/lifecycle=spot"
      override_instance_types              = var.eks_instance_types
      target_group_arns                    = module.nlb.target_group_arns
      tags = [
        {
          key                 = "aws-node-termination-handler/managed"
          value               = true
          propagate_at_launch = true
        },
        {
          key                 = "Environment"
          value               = var.environment
          propagate_at_launch = true
        },
        {
          key                 = "k8s.io/cluster-autoscaler/enabled"
          value               = true
          propagate_at_launch = true
        },
        {
          key                 = "k8s.io/cluster-autoscaler/${local.identifier}"
          value               = "owned"
          propagate_at_launch = true
        }
      ]
    }
  ]

  # Kubernetes configurations
  write_kubeconfig = false
  # Give both roles admin access due to the need for the OIDC assumable role and the basic assumable role. The bastion
  # host does not seem to support the OIDC role at all so a second one was required.
  map_roles = [ # aws-auth configmap
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

  tags = local.tags
}