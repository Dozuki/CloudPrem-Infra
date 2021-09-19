terraform {
  required_providers {
    aws = "3.56.0"
  }
}

provider "kubernetes" {
  host                   = data.aws_eks_cluster.main.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority[0].data)
  token                  = data.aws_eks_cluster_auth.main.token
}

locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-dozuki-${var.environment}"

  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Identifier  = var.identifier
    Environment = var.environment
  }
}
data "aws_eks_cluster" "main" {
  name = module.eks_cluster.cluster_id
}
data "aws_eks_cluster_auth" "main" {
  name = module.eks_cluster.cluster_id
}

data "aws_partition" "current" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
data "aws_subnets" "private" {
  filter {
    name   = "vpc-id"
    values = [var.vpc_id]
  }

  tags = {
    type = "private"
  }
}
data "aws_subnets" "public" {
  filter {
    name   = "vpc-id"
    values = [var.vpc_id]
  }

  tags = {
    type = "public"
  }
}
data "aws_kms_key" "s3" {
  key_id = var.kms_key_id
}


# IAM role to access the EKS cluster. By default only the user that creates the cluster has access to it
module "cluster_access_role" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role"
  version = "4.3.0"

  create_role = true

  role_name         = "${local.identifier}-${data.aws_region.current.name}-cluster-access"
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
resource "aws_launch_template" "eks" {
  name_prefix            = "eks-worker-"
  description            = "Default Launch-Template"
  update_default_version = true

  block_device_mappings {
    device_name = "/dev/xvda"

    ebs {
      volume_size           = var.eks_volume_size
      volume_type           = "gp2"
      delete_on_termination = true
      # encrypted             = true

      # Enable this if you want to encrypt your node root volumes with a KMS/CMK. encryption of PVCs is handled via k8s StorageClass tho
      # you also need to attach data.aws_iam_policy_document.ebs_decryption.json from the disk_encryption_policy.tf to the KMS/CMK key then !!
      # kms_key_id            = var.kms_key_arn
    }
  }

  monitoring {
    enabled = true
  }

  network_interfaces {
    associate_public_ip_address = false
    delete_on_termination       = true
    security_groups             = [module.eks_cluster.worker_security_group_id]
  }


  # Supplying custom tags to EKS instances is another use-case for LaunchTemplates
  tag_specifications {
    resource_type = "instance"

    tags = local.tags
  }

  # Supplying custom tags to EKS instances root volumes is another use-case for LaunchTemplates. (doesnt add tags to dynamically provisioned volumes via PVC tho)
  tag_specifications {
    resource_type = "volume"

    tags = local.tags
  }

  # Supplying custom tags to EKS instances ENI's is another use-case for LaunchTemplates
  tag_specifications {
    resource_type = "network-interface"

    tags = local.tags
  }

  # Tag the LT itself

  tags = local.tags

  lifecycle {
    create_before_destroy = true
  }
}

#tfsec:ignore:aws-vpc-no-public-egress-sgr
#tfsec:ignore:aws-eks-no-public-cluster-access-to-cidr
#tfsec:ignore:aws-eks-no-public-cluster-access
#tfsec:ignore:aws-eks-encrypt-secrets
#tfsec:ignore:aws-eks-enable-control-plane-logging
module "eks_cluster" {
  source  = "terraform-aws-modules/eks/aws"
  version = "17.11.0"

  depends_on = [aws_iam_policy.cluster_access, aws_iam_policy.eks_worker]

  # EKS cofigurations
  cluster_name                    = local.identifier
  cluster_version                 = "1.20"
  enable_irsa                     = true
  cluster_endpoint_public_access  = !var.protect_resources
  cluster_endpoint_private_access = true

  vpc_id  = var.vpc_id
  subnets = data.aws_subnets.private.ids

  workers_role_name = "${local.identifier}-${data.aws_region.current.name}-worker"

  workers_additional_policies = [
    aws_iam_policy.eks_worker.arn,
  ]

  node_groups = {
    workers = {
      desired_capacity = var.eks_desired_capacity
      max_capacity     = var.eks_max_size
      min_capacity     = var.eks_min_size
      instance_types   = [var.eks_instance_type]

      launch_template_id      = aws_launch_template.eks.id
      launch_template_version = aws_launch_template.eks.default_version

      k8s_labels = {
        Environment = var.environment
      }

      additional_tags = {
        Environment = var.environment
      }
    }
  }
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

resource "aws_security_group_rule" "replicated_ui_access" {
  type              = "ingress"
  from_port         = 32001
  to_port           = 32001
  protocol          = "tcp"
  cidr_blocks       = [var.replicated_ui_access_cidr] #tfsec:ignore:AWS006
  security_group_id = module.eks_cluster.worker_security_group_id
  description       = "Access to the replicated UI"
}

resource "aws_security_group_rule" "app_access_https" {
  type              = "ingress"
  from_port         = 32005
  to_port           = 32005
  protocol          = "tcp"
  cidr_blocks       = [var.app_access_cidr] #tfsec:ignore:AWS006
  security_group_id = module.eks_cluster.worker_security_group_id
  description       = "Access to application"
}

resource "aws_security_group_rule" "app_access_http" {
  type              = "ingress"
  from_port         = 32010
  to_port           = 32010
  protocol          = "tcp"
  cidr_blocks       = [var.app_access_cidr] #tfsec:ignore:AWS006
  security_group_id = module.eks_cluster.worker_security_group_id
  description       = "Access to application"
}

#tfsec:ignore:aws-elbv2-alb-not-public
module "nlb" {
  source  = "terraform-aws-modules/alb/aws"
  version = "6.5.0"

  name = local.identifier

  load_balancer_type = "network"
  internal           = !var.public_access

  vpc_id  = var.vpc_id
  subnets = data.aws_subnets.public.ids

  target_groups = [
    {
      name_prefix      = "rep-"
      backend_protocol = "TCP"
      backend_port     = 32001
      target_type      = "instance"
    },
    {
      name_prefix      = "app-"
      backend_protocol = "TCP"
      backend_port     = 32005
      target_type      = "instance"
    },
    {
      name_prefix      = "http-"
      backend_protocol = "TCP"
      backend_port     = 32010
      target_type      = "instance"
    }
  ]

  http_tcp_listeners = [
    {
      port               = 8800
      protocol           = "TCP"
      target_group_index = 0
    },
    {
      port               = 443
      protocol           = "TCP"
      target_group_index = 1
    },
    {
      port               = 80
      protocol           = "TCP"
      target_group_index = 2
    }
  ]

  tags = local.tags
}

resource "aws_autoscaling_attachment" "autoscaling_attachment" {

  count = length(module.nlb.target_group_arns)

  autoscaling_group_name = lookup(lookup(lookup(module.eks_cluster.node_groups["workers"], "resources")[0], "autoscaling_groups")[0], "name")
  alb_target_group_arn   = module.nlb.target_group_arns[count.index]
}
