data "aws_kms_key" "eks" {
  count = local.create_eks_kms ? 0 : 1

  key_id = var.eks_kms_key_id
}

# IAM role that the bastion host can assume.
module "cluster_access_role_assumable" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role"
  version = "~> 6.0"

  create = true

  name            = "${local.identifier}-${data.aws_region.current.region}-cluster-access-assumable"
  use_name_prefix = false

  policies = {
    ReadOnlyAccess = "arn:${data.aws_partition.current.partition}:iam::aws:policy/ReadOnlyAccess"
    ClusterAccess  = aws_iam_policy.cluster_access.arn
  }

  trust_policy_permissions = {
    assume = {
      effect = "Allow"
      principals = [{
        type        = "AWS"
        identifiers = ["arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root"]
      }]
      actions = ["sts:AssumeRole"]
    }
  }

  tags = local.tags
}

data "aws_iam_policy_document" "cluster_access" {
  statement {
    actions = [
      "eks:AccessKubernetesApi",
    ]

    resources = [
      "arn:${data.aws_partition.current.partition}:eks:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:cluster/${local.identifier}",
    ]
  }
}

resource "aws_iam_policy" "cluster_access" {
  name   = "${local.identifier}-${data.aws_region.current.region}-cluster-access"
  policy = data.aws_iam_policy_document.cluster_access.json

  tags = local.tags
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

    resources = [local.s3_kms_key_id]
  }

  # Objects written before this stack owned its key stay encrypted under the
  # donor key forever - S3 records the key on the object at write time, and
  # noncurrent versions are never rewritten. Without this the app loses access
  # to everything it wrote previously the moment the bucket default flips.
  #
  # Decrypt only, and deliberately a separate statement: the donor key must
  # never encrypt anything new, which is the whole point of this stack owning
  # its own key. Folding these resources into the statement above would hand
  # the app kms:Encrypt and kms:GenerateDataKey on a key we do not own.
  #
  # Produces no statement at all when s3_kms_key_id is unset, the normal case.
  dynamic "statement" {
    for_each = data.aws_kms_key.s3_migration

    content {
      actions   = ["kms:Decrypt", "kms:DescribeKey"]
      resources = [statement.value.arn]
    }
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
      "s3:DeleteObject",
      "s3:DeleteObjectVersion",
      "s3:DeleteObjectTagging",
      "s3:ReplicateTags",
      "s3:PutObjectVersionTagging",
      "s3:PutObjectTagging",
      "s3:DeleteObjectVersionTagging"
    ]

    resources = flatten([
      for bucket in aws_s3_bucket.guide_buckets : [
        bucket.arn,
        "${bucket.arn}/*",
      ]
    ])
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
      # DescribeReplicationTasks is required alongside Start: the dms-start Job
      # (logical/bi.tf) reads the task's current status FIRST and only starts it when
      # it is ready/stopped/failed, since start-replication-task errors on a task that
      # is already running. Without Describe the Job dies on AccessDenied before it can
      # decide, and because it runs with wait_for_completion = false the apply still
      # reports success while DMS silently never starts.
      #
      # This was masked until now: the Job's image shipped a 2019 AWS CLI that could not
      # use EKS Pod Identity at all, so it never got far enough to be denied.
      "dms:DescribeReplicationTasks",
      # DMS will not start a task whose endpoints have never had a successful
      # test-connection, which is always true on a fresh stack. The Job therefore has to
      # test both endpoints and poll for the result before starting.
      "dms:DescribeConnections",
      "dms:TestConnection",
      "dms:StartReplicationTask",
      # The serverless equivalents. BI is a replication CONFIG now (physical/bi.tf), which is
      # a different API surface: describe-replication-tasks does not return it and
      # start-replication-task cannot start it. The Job branches on the ARN shape and needs
      # both sets, because a stack mid-swap can still be on the provisioned task.
      # Without these it fails on AccessDenied, and with wait_for_completion = false that
      # failure does not reach the run - which is how the old CLI-too-old bug hid for years.
      "dms:DescribeReplicationConfigs",
      "dms:DescribeReplications",
      "dms:StartReplication"
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
  name   = "${local.identifier}-${data.aws_region.current.region}"
  policy = data.aws_iam_policy_document.eks_worker.json
}

# We need separate policies to maintain backwards compatibility with existing stacks. Modifying the existing policy
# with new resources triggers a cluster breaking event.
resource "aws_iam_policy" "eks_worker_kms" {
  name   = "${local.identifier}-${data.aws_region.current.region}-kms"
  policy = data.aws_iam_policy_document.eks_worker_kms.json
}

resource "aws_kms_key" "eks" {
  count = local.create_eks_kms ? 1 : 0

  description             = "EKS Secret Encryption Key"
  enable_key_rotation     = true
  deletion_window_in_days = var.protect_resources ? 30 : 7

  tags = local.tags
}

resource "aws_iam_policy" "assume_cross_account_role" {
  name        = "${local.identifier}-${data.aws_region.current.region}-AssumeCrossAccountRole"
  description = "Policy to assume the cross-account role for Route 53 hosted zone access"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Action   = ["sts:AssumeRole", "sts:TagSession"]
        Effect   = "Allow"
        Resource = local.route_53_role
      }
    ]
  })

  tags = local.tags
}

#tfsec:ignore:aws-vpc-no-public-egress-sgr
#tfsec:ignore:aws-eks-no-public-cluster-access-to-cidr
#tfsec:ignore:aws-eks-no-public-cluster-access
#tfsec:ignore:aws-eks-encrypt-secrets
#tfsec:ignore:aws-eks-enable-control-plane-logging
module "eks_cluster" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 21.0"

  # NO module-level depends_on here, ever. It defers EVERY data source inside
  # the module to apply time whenever a dependency has ANY pending diff, which
  # turns partition/caller-identity-derived attributes into (known after apply)
  # and force-replaces the cluster's access entries and IAM policy attachments.
  # The 2026-07-15 fleet default_tags rollout tripped exactly this on every EKS
  # stack (the old depends_on pointed at two IAM policies that are not even
  # module inputs - they attach to other roles, so no ordering was ever needed).

  # The default 15m cluster-delete timeout can be exceeded when the cluster still has
  # load-balancer/ENI resources to clean up (e.g. an automated teardown after a failed
  # logical destroy leaves Service-type LoadBalancers behind). Give it headroom so the
  # teardown completes instead of timing out mid-DELETING and stranding the VPC.
  timeouts = {
    delete = "30m"
  }

  name = local.identifier
  # Default null lets EKS Auto Mode manage version via upgrade_policy.
  # Set eks_k8s_version to pin a specific version if needed.
  kubernetes_version = var.eks_k8s_version
  enable_irsa        = true

  # Auto-upgrade the cluster at end of standard support to avoid extended support costs.
  upgrade_policy = {
    support_type = "STANDARD"
  }

  # Need public access even when deploying from AWS due to the occasional inability to access private endpoints.
  endpoint_public_access  = true
  endpoint_private_access = true

  # Control-plane log streaming to CloudWatch (audit, api, authenticator,
  # controllerManager, scheduler). Required by SOC2 / Vanta and other
  # compliance frameworks. Configurable via var.eks_enabled_log_types;
  # default is all 5. Retention defaults to 90 days (module default).
  enabled_log_types = var.eks_enabled_log_types

  encryption_config = {
    provider_key_arn = local.eks_kms_key
    resources        = ["secrets"]
  }

  # Auto Mode: Karpenter-based scaling, built-in EBS CSI, LB controller, and spot interruption handling.
  # bootstrap_self_managed_addons defaults to false when compute_config is enabled, triggering cluster replacement.
  compute_config = {
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
        principal_arn = module.cluster_access_role_assumable.arn
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
  name = "${local.identifier}-${data.aws_region.current.region}-app-pod-identity"

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

resource "aws_eks_pod_identity_association" "app_default" {
  cluster_name    = module.eks_cluster.cluster_name
  namespace       = "dozuki"
  service_account = "default"
  role_arn        = aws_iam_role.app_pod_identity.arn

  tags = local.tags
}

# App deployments use the migration-wait SA (for kubectl RBAC in init
# containers). Pod Identity on EKS 1.35+ strictly matches SA names.
resource "aws_eks_pod_identity_association" "app_migration_wait" {
  cluster_name    = module.eks_cluster.cluster_name
  namespace       = "dozuki"
  service_account = "dozuki-migration-wait"
  role_arn        = aws_iam_role.app_pod_identity.arn

  tags = local.tags
}

# Pod Identity: cert-manager cross-account Route53
resource "aws_iam_role" "cert_manager_pod_identity" {
  name = "${local.identifier}-${data.aws_region.current.region}-cert-manager-pod-identity"

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

  tags = local.tags
}

# Log shipping: Fluent Bit (amazon-cloudwatch-observability addon).
#
# EKS Auto Mode gives nodes a deliberately minimal role (only EKS worker + ECR pull),
# so Fluent Bit must get its CloudWatch permissions via Pod Identity. Without an
# association it falls back to the node role and every logs:PutLogEvents is
# AccessDenied - container logs silently stop reaching CloudWatch, which is now the
# only thing this addon does.
#
# Fluent Bit runs as the cloudwatch-agent SA (the name is historical), so this one
# association still covers it even though the metrics agent it was named for is gone:
# the addon config sets agents = [] and metrics come from Prometheus into Mimir now
# (logical/kubernetes.tf). The role keeps cloudwatch:PutMetricData because the
# managed CloudWatchAgentServerPolicy grants it; that is harmless with no agent
# publishing, and swapping to a narrower logs-only policy is a separate change.
resource "aws_iam_role" "cloudwatch_agent_pod_identity" {
  name = "${local.identifier}-${data.aws_region.current.region}-cw-agent-pod-identity"

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

resource "aws_iam_role_policy_attachment" "cloudwatch_agent_server" {
  role       = aws_iam_role.cloudwatch_agent_pod_identity.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/CloudWatchAgentServerPolicy"
}

# CloudWatch Observability: the EKS ADDON itself is created in the LOGICAL layer,
# because on EKS Auto Mode a fresh cluster has zero nodes until a workload is
# scheduled — the addon's agent/DaemonSets can't go healthy until cert-manager/the
# app trigger node provisioning, so installing it here (physical, pre-nodes) makes
# it sit DEGRADED and time out. Physical keeps only the IAM role (above) and the
# pod-identity association that wires it to the addon's cloudwatch-agent service
# account. The association is created by (cluster, namespace, SA) name and is valid
# before the SA/namespace exist — EKS applies it once the logical addon installs them.
resource "aws_eks_pod_identity_association" "cloudwatch_agent" {
  cluster_name    = module.eks_cluster.cluster_name
  namespace       = "amazon-cloudwatch"
  service_account = "cloudwatch-agent"
  role_arn        = aws_iam_role.cloudwatch_agent_pod_identity.arn

  tags = local.tags
}

# CloudWatch exporter (the chart's monitoring.cloudwatchExporter deployment).
#
# Pulls two AWS/EC2 metrics into Prometheus so we can alert on EBS burst-credit
# exhaustion. That failure is invisible to Kubernetes: the node keeps reporting
# Ready and StorageReady=True (StorageReady has no fatal reason, so it can never
# go False) while every disk-backed pod on it stalls. Nothing in-cluster moves,
# so nothing in-cluster can alert. Found the hard way on a node that sat
# IO-starved for five hours with no signal anywhere.
#
# Read-only, and narrower than it looks. Discovery goes through the Resource
# Groups Tagging API, so this needs no ec2:Describe* at all - measured against a
# live account, a full poll cycle is 1 GetMetricData, 2 ListMetrics and 1 tagging
# call. None of these four actions support resource-level permissions.
resource "aws_iam_role" "cloudwatch_exporter_pod_identity" {
  name = "${local.identifier}-${data.aws_region.current.region}-cw-exporter-pod-identity"

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

resource "aws_iam_role_policy" "cloudwatch_exporter_read" {
  name = "cloudwatch-read"
  role = aws_iam_role.cloudwatch_exporter_pod_identity.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["cloudwatch:GetMetricData", "cloudwatch:ListMetrics"]
        Resource = "*"
      },
      {
        # Instance discovery. This is what replaces ec2:DescribeInstances.
        Effect   = "Allow"
        Action   = ["tag:GetResources"]
        Resource = "*"
      },
      {
        # Only used to decorate the series with the account alias, so losing it
        # costs nothing but a log line. Include it anyway: without it the
        # exporter logs an AccessDenied WARN on every single poll cycle, forever.
        Effect   = "Allow"
        Action   = ["iam:ListAccountAliases"]
        Resource = "*"
      }
    ]
  })
}

# Same layering rule as the cloudwatch-agent association above: the workload is
# a logical-layer concern, the IAM is not. The (cluster, namespace, SA) tuple is
# valid before any of them exist, so this can be created here and simply starts
# working once the chart schedules the pod.
#
# The namespace and SA name are the logical layer's release name and namespace
# ("dozuki" both, see logical/main.tf k8s_namespace_name and flux.tf
# releaseName), and the chart derives the SA as <release>-cloudwatch-exporter.
# Renaming the release breaks this silently - the pod would run with no
# credentials and fail every poll.
resource "aws_eks_pod_identity_association" "cloudwatch_exporter" {
  cluster_name    = module.eks_cluster.cluster_name
  namespace       = "dozuki"
  service_account = "dozuki-cloudwatch-exporter"
  role_arn        = aws_iam_role.cloudwatch_exporter_pod_identity.arn

  tags = local.tags
}

# metrics-server is now provided by the dozuki chart (metrics-server.enabled, default
# on) so it's a single source of truth across onprem and cloud - see the chart's
# values.yaml. The EKS managed addon was retired here; the chart install carries
# metrics-server on every platform, and this layer drops the chart's onprem-oriented
# --kubelet-insecure-tls arg for cloud (EKS kubelets present proper serving certs) via
# the app release's metrics-server.args override in logical/flux.tf (app_base_values).
