data "aws_kms_key" "eks" {
  count = local.create_eks_kms ? 0 : 1

  key_id = var.eks_kms_key_id
}

# IAM role that the bastion host can assume.
module "cluster_access_role_assumable" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role"
  version = "5.11.2"

  create_role = true

  role_name         = "${local.identifier}-${data.aws_region.current.name}-cluster-access-assumable"
  role_requires_mfa = false

  custom_role_policy_arns = [
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/ReadOnlyAccess",
    aws_iam_policy.cluster_access.arn
  ]

  trusted_role_arns = [
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

data "aws_iam_policy_document" "eks_worker_kms" {
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
      local.s3_kms_key_id,
    ]
  }
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
      "s3:CopyObject",
      "s3:DeleteObjectTagging",
      "s3:ReplicateTags",
      "s3:PutObjectVersionTagging",
      "s3:PutObjectTagging",
      "s3:DeleteObjectVersionTagging"
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

    # This is done to maintain backwards compatibility with <=3.1.
    # The actual KMS permissions exist in the `eks_worker_kms` policy resource.
    resources = [
      data.aws_kms_key.s3_default.arn
    ]
  }

  statement {
    actions = [
      "rds:CreateDBSnapshot",
      "rds:DescribeDBSnapshots",
      "rds:AddTagsToResource"
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

  statement {
    actions = [
      "ecr:GetAuthorizationToken",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
      "ecr:ListImages"
    ]

    resources = ["*"]
  }
}

resource "aws_iam_policy" "eks_worker" {
  name   = "${local.identifier}-${data.aws_region.current.name}"
  policy = data.aws_iam_policy_document.eks_worker.json
}

# We need separate policies to maintain backwards compatibility with existing stacks. Modifying the existing policy
# with new resources triggers a cluster breaking event.
resource "aws_iam_policy" "eks_worker_kms" {
  name   = "${local.identifier}-${data.aws_region.current.name}-kms"
  policy = data.aws_iam_policy_document.eks_worker_kms.json
}

resource "aws_kms_key" "eks" {
  count = local.create_eks_kms ? 1 : 0

  description         = "EKS Secret Encryption Key"
  enable_key_rotation = true
}

resource "aws_iam_policy" "assume_cross_account_role" {
  name        = "${local.identifier}-${data.aws_region.current.name}-AssumeCrossAccountRole"
  description = "Policy to assume the cross-account role for Route 53 hosted zone access"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Action   = "sts:AssumeRole"
        Effect   = "Allow"
        Resource = local.route_53_role
      }
    ]
  })
}

#tfsec:ignore:aws-vpc-no-public-egress-sgr
#tfsec:ignore:aws-eks-no-public-cluster-access-to-cidr
#tfsec:ignore:aws-eks-no-public-cluster-access
#tfsec:ignore:aws-eks-encrypt-secrets
#tfsec:ignore:aws-eks-enable-control-plane-logging
module "eks_cluster" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"

  depends_on = [aws_iam_policy.cluster_access, aws_iam_policy.eks_worker]

  cluster_name    = local.identifier
  cluster_version = var.eks_k8s_version
  enable_irsa     = true

  # Auto-upgrade the cluster at end of standard support to avoid extended support costs.
  cluster_upgrade_policy = {
    support_type = "STANDARD"
  }

  # Need public access even when deploying from AWS due to the occasional inability to access private endpoints.
  cluster_endpoint_public_access  = true
  cluster_endpoint_private_access = true

  cluster_encryption_config = {
    provider_key_arn = local.eks_kms_key
    resources        = ["secrets"]
  }

  # Auto Mode: Karpenter-based scaling, built-in EBS CSI, LB controller, and spot interruption handling.
  # bootstrap_self_managed_addons defaults to false when compute_config is enabled, triggering cluster replacement.
  cluster_compute_config = {
    enabled    = true
    node_pools = ["system"]
  }

  vpc_id     = local.vpc_id
  subnet_ids = local.private_subnet_ids

  # Access entries replace the old map_roles / aws-auth configmap management.
  authentication_mode                      = "API_AND_CONFIG_MAP"
  enable_cluster_creator_admin_permissions = true

  access_entries = merge(
    {
      cluster_access_assumable = {
        principal_arn = module.cluster_access_role_assumable.iam_role_arn
        policy_associations = {
          admin = {
            policy_arn   = "arn:${data.aws_partition.current.partition}:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
            access_scope = { type = "cluster" }
          }
        }
      }
    },
    # SSO admin access for kubectl/Lens from workstations.
    # Access entries require an exact role ARN — wildcards are not supported.
    var.sso_admin_role_arn != "" ? {
      sso_admin = {
        principal_arn = var.sso_admin_role_arn
        policy_associations = {
          admin = {
            policy_arn   = "arn:${data.aws_partition.current.partition}:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
            access_scope = { type = "cluster" }
          }
        }
      }
    } : {}
  )

  tags = local.tags
}

# Pod Identity: App workloads (S3, KMS, RDS, DMS, logs, ECR)
resource "aws_iam_role" "app_pod_identity" {
  name = "${local.identifier}-${data.aws_region.current.name}-app-pod-identity"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Service = "pods.eks.amazonaws.com"
        }
        Action = [
          "sts:AssumeRole",
          "sts:TagSession"
        ]
      }
    ]
  })

  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "app_pod_identity_worker" {
  role       = aws_iam_role.app_pod_identity.name
  policy_arn = aws_iam_policy.eks_worker.arn
}

resource "aws_iam_role_policy_attachment" "app_pod_identity_worker_kms" {
  role       = aws_iam_role.app_pod_identity.name
  policy_arn = aws_iam_policy.eks_worker_kms.arn
}

resource "aws_eks_pod_identity_association" "app" {
  cluster_name    = module.eks_cluster.cluster_name
  namespace       = "dozuki"
  service_account = "default"
  role_arn        = aws_iam_role.app_pod_identity.arn
}

# Pod Identity: cert-manager cross-account Route53
resource "aws_iam_role" "cert_manager_pod_identity" {
  name = "${local.identifier}-${data.aws_region.current.name}-cert-manager-pod-identity"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Service = "pods.eks.amazonaws.com"
        }
        Action = [
          "sts:AssumeRole",
          "sts:TagSession"
        ]
      }
    ]
  })

  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "cert_manager_pod_identity" {
  role       = aws_iam_role.cert_manager_pod_identity.name
  policy_arn = aws_iam_policy.assume_cross_account_role.arn
}

resource "aws_eks_pod_identity_association" "cert_manager" {
  cluster_name    = module.eks_cluster.cluster_name
  namespace       = "cert-manager"
  service_account = "cert-manager"
  role_arn        = aws_iam_role.cert_manager_pod_identity.arn
}

# CloudWatch Observability add-on (replaces ADOT + Fluent Bit)
resource "aws_eks_addon" "cloudwatch_observability" {
  cluster_name = module.eks_cluster.cluster_name
  addon_name   = "amazon-cloudwatch-observability"
}

# Metrics Server add-on (provides metrics.k8s.io API for kubectl top, HPA, k9s, Lens)
resource "aws_eks_addon" "metrics_server" {
  cluster_name = module.eks_cluster.cluster_name
  addon_name   = "metrics-server"
}
