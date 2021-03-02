data "aws_eks_cluster" "cluster" {
  name = module.eks_cluster.cluster_id
}

data "aws_eks_cluster_auth" "cluster" {
  name = module.eks_cluster.cluster_id
}

provider "kubernetes" {
  host                   = data.aws_eks_cluster.cluster.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.cluster.certificate_authority.0.data)
  token                  = data.aws_eks_cluster_auth.cluster.token
  load_config_file       = false
}

provider "helm" {
  kubernetes {
    host                   = data.aws_eks_cluster.cluster.endpoint
    cluster_ca_certificate = base64decode(data.aws_eks_cluster.cluster.certificate_authority.0.data)
    token                  = data.aws_eks_cluster_auth.cluster.token
  }
}

# IAM role to access the EKS cluster. By default only the user that creates the cluster has access to it
module "cluster_access_role" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role"
  version = "3.6.0"

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

data "aws_iam_policy_document" "eks_worker" {
  statement {
    actions = [
      "s3:PutObject",
      "s3:GetObject",
    ]

    resources = [
      "arn:${data.aws_partition.current.partition}:s3:::*",
    ]
  }

  statement {
    actions = [
      "kms:Encrypt",
      "kms:Decrypt",
      "kms:ReEncrypt*",
      "kms:GenerateDataKey*",
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

module "eks_cluster" {
  source  = "terraform-aws-modules/eks/aws"
  version = "14.0.0"

  depends_on = [module.vpc]

  # EKS cofigurations
  cluster_name    = local.identifier
  cluster_version = "1.18"
  enable_irsa     = true

  vpc_id  = local.vpc_id
  subnets = local.private_subnet_ids

  workers_role_name = "${local.identifier}-${data.aws_region.current.name}-worker"

  workers_additional_policies = [
    aws_iam_policy.eks_worker.arn,
  ]

  node_groups_defaults = {
    instance_types = [var.eks_instance_type]
    disk_size      = var.eks_volume_size
  }

  node_groups = {
    workers = {
      desired_capacity = var.eks_desired_capacity
      max_capacity     = var.eks_max_size
      min_capacity     = var.eks_min_size

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
      rolearn  = module.cluster_access_role.this_iam_role_arn
      username = "admin"
      groups   = ["system:masters"]
    },
  ]

  tags = local.tags
}

resource "null_resource" "managed_node_asg_nlb_attach" { # TODO Remove when feature is added https://github.com/aws/containers-roadmap/issues/709

  triggers = {
    asg = module.eks_cluster.node_groups["workers"].resources[0].autoscaling_groups[0].name
  }

  provisioner "local-exec" {
    command = "aws autoscaling attach-load-balancer-target-groups --auto-scaling-group-name '${module.eks_cluster.node_groups["workers"].resources[0].autoscaling_groups[0].name}' --target-group-arns ${join(" ", module.nlb.target_group_arns)}"
  }
}

resource "aws_security_group_rule" "replicated_ui_access" {
  type              = "ingress"
  from_port         = 32001
  to_port           = 32001
  protocol          = "tcp"
  cidr_blocks       = [var.replicated_ui_access_cidr] #tfsec:ignore:AWS006
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "Access to the replicated UI"
}

resource "aws_security_group_rule" "app_access_https" {
  type              = "ingress"
  from_port         = 32005
  to_port           = 32005
  protocol          = "tcp"
  cidr_blocks       = [var.app_access_cidr] #tfsec:ignore:AWS006
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "Access to application"
}

resource "aws_security_group_rule" "app_access_http" {
  type              = "ingress"
  from_port         = 32010
  to_port           = 32010
  protocol          = "tcp"
  cidr_blocks       = [var.app_access_cidr] #tfsec:ignore:AWS006
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "Access to application"
}

module "nlb" {
  source  = "terraform-aws-modules/alb/aws"
  version = "5.10.0"

  name = local.identifier

  load_balancer_type = "network"
  internal           = !var.public_access

  vpc_id  = local.vpc_id
  subnets = local.public_subnet_ids

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

module "cpu_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "1.3.0"

  alarm_name        = "${local.identifier}-cpu-high"
  alarm_description = "CPU utilization high for ${local.identifier} worker nodes"

  namespace   = "AWS/EC2"
  metric_name = "CPUUtilization"
  statistic   = "Average"

  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  threshold           = 90
  period              = 60

  dimensions = {
    AutoScalingGroupName = module.eks_cluster.node_groups["workers"].resources[0].autoscaling_groups[0].name
  }

  alarm_actions = [
    module.sns.this_sns_topic_arn
  ]

  ok_actions = [
    module.sns.this_sns_topic_arn
  ]
}

module "memory_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "1.3.0"

  alarm_name        = "${local.identifier}-memory-high"
  alarm_description = "Memory utilization high for ${local.identifier} cluster"

  namespace   = "ContainerInsights"
  metric_name = "node_memory_utilization"
  statistic   = "Average"

  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  threshold           = 75
  period              = 120

  dimensions = {
    ClusterName = module.eks_cluster.cluster_id
  }

  alarm_actions = [
    module.sns.this_sns_topic_arn
  ]

  ok_actions = [
    module.sns.this_sns_topic_arn
  ]
}

module "status_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "1.3.0"

  alarm_name        = "${local.identifier}-status"
  alarm_description = "Status check for ${local.identifier} cluster"

  namespace   = "AWS/EC2"
  metric_name = "StatusCheckFailed"
  statistic   = "Average"

  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  threshold           = 1
  period              = 60

  dimensions = {
    AutoScalingGroupName = module.eks_cluster.node_groups["workers"].resources[0].autoscaling_groups[0].name
  }

  alarm_actions = [
    module.sns.this_sns_topic_arn
  ]

  ok_actions = [
    module.sns.this_sns_topic_arn
  ]
}

module "nodes_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "1.3.0"

  alarm_name        = "${local.identifier}-nodes-in-service"
  alarm_description = "Nodes in service under desired capacity for ${local.identifier} cluster"

  namespace   = "AWS/AutoScaling"
  metric_name = "GroupInServiceInstances"
  statistic   = "Sum"

  comparison_operator = "LessThanThreshold"
  evaluation_periods  = 1
  threshold           = var.eks_desired_capacity
  period              = 60

  dimensions = {
    AutoScalingGroupName = module.eks_cluster.node_groups["workers"].resources[0].autoscaling_groups[0].name
  }

  alarm_actions = [
    module.sns.this_sns_topic_arn
  ]

  ok_actions = [
    module.sns.this_sns_topic_arn
  ]
}