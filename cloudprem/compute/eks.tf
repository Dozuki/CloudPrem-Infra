
# IAM role to access the EKS cluster. By default only the user that creates the cluster has access to it
module "cluster_access_role" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc"
  version = "4.3.0"

  create_role = true
  role_name   = local.cluster_access_role_name

  provider_url = replace(module.eks_cluster.cluster_oidc_issuer_url, "https://", "")
  role_policy_arns = [
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/ReadOnlyAccess",
    aws_iam_policy.cluster_access.arn,
    aws_iam_policy.cluster_autoscaler.arn
  ]
  oidc_fully_qualified_subjects = [
    "system:serviceaccount:kube-system:cluster-autoscaler-aws-cluster-autoscaler-chart",
    "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root"
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
}

resource "aws_iam_policy" "eks_worker" {
  name   = "${local.identifier}-${data.aws_region.current.name}"
  policy = data.aws_iam_policy_document.eks_worker.json
}
//resource "aws_launch_template" "eks" {
//  name_prefix            = "eks-worker-"
//  description            = "Default Launch-Template"
//  update_default_version = true
//
//  block_device_mappings {
//    device_name = "/dev/xvda"
//
//    ebs {
//      volume_size           = var.eks_volume_size
//      volume_type           = "gp2"
//      delete_on_termination = true
//      # encrypted             = true
//
//      # Enable this if you want to encrypt your node root volumes with a KMS/CMK. encryption of PVCs is handled via k8s StorageClass tho
//      # you also need to attach data.aws_iam_policy_document.ebs_decryption.json from the disk_encryption_policy.tf to the KMS/CMK key then !!
//      # kms_key_id            = var.kms_key_arn
//    }
//  }
//
//  monitoring {
//    enabled = true
//  }
//
//  network_interfaces {
//    associate_public_ip_address = false
//    delete_on_termination       = true
//    security_groups             = [module.eks_cluster.worker_security_group_id]
//  }
//
//
//  # Supplying custom tags to EKS instances is another use-case for LaunchTemplates
//  tag_specifications {
//    resource_type = "instance"
//
//    tags = local.tags
//  }
//
//  # Supplying custom tags to EKS instances root volumes is another use-case for LaunchTemplates. (doesnt add tags to dynamically provisioned volumes via PVC tho)
//  tag_specifications {
//    resource_type = "volume"
//
//    tags = local.tags
//  }
//
//  # Supplying custom tags to EKS instances ENI's is another use-case for LaunchTemplates
//  tag_specifications {
//    resource_type = "network-interface"
//
//    tags = local.tags
//  }
//
//  # Tag the LT itself
//
//  tags = local.tags
//
//  lifecycle {
//    create_before_destroy = true
//  }
//}

#tfsec:ignore:aws-vpc-no-public-egress-sgr
#tfsec:ignore:aws-eks-no-public-cluster-access-to-cidr
#tfsec:ignore:aws-eks-no-public-cluster-access
#tfsec:ignore:aws-eks-encrypt-secrets
#tfsec:ignore:aws-eks-enable-control-plane-logging
module "eks_cluster" {
  source  = "terraform-aws-modules/eks/aws"
  version = "17.20.0"

  depends_on = [aws_iam_policy.cluster_access, aws_iam_policy.eks_worker]

  # EKS cofigurations
  cluster_name                    = local.identifier
  cluster_version                 = "1.21"
  enable_irsa                     = true
  cluster_endpoint_public_access  = !var.protect_resources
  cluster_endpoint_private_access = true

  vpc_id  = var.vpc_id
  subnets = data.aws_subnets.private.ids

  //  workers_role_name = "${local.identifier}-${data.aws_region.current.name}-worker"

  workers_additional_policies = [
    aws_iam_policy.eks_worker.arn,
  ]
  worker_groups_launch_template = [
    {
      name                                 = "workers"
      asg_max_size                         = 10
      asg_desired_capacity                 = 3
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
          value               = ""
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

  //  node_groups = {
  //    workers = {
  //      name = "${local.identifier}-workers"
  //      desired_capacity = var.eks_desired_capacity
  //      max_capacity     = var.eks_max_size
  //      min_capacity     = var.eks_min_size
  //      instance_types   = var.eks_instance_types
  //      capacity_type  = "SPOT"
  //
  //      launch_template_id      = aws_launch_template.eks.id
  //      launch_template_version = aws_launch_template.eks.default_version
  //
  //      k8s_labels = {
  //        Environment = var.environment
  //      }
  //
  //      additional_tags = {
  //        Environment = var.environment,
  //        "k8s.io/cluster-autoscaler/enabled" = true
  //        "k8s.io/cluster-autoscaler/${local.identifier}" = "owned"
  //      }
  //    }
  //  }
  # Kubernetes configurations
  write_kubeconfig = false

  map_roles = [ # aws-auth configmap
    {
      rolearn  = module.cluster_access_role.iam_role_arn
      username = "admin"
      groups   = ["system:masters"]
    },
  ]

  tags = local.tags
}


