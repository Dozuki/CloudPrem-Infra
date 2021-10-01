
data "aws_iam_policy_document" "aws_node_termination_handler" {
  statement {
    effect = "Allow"
    actions = [
      "ec2:DescribeInstances",
      "autoscaling:DescribeAutoScalingInstances",
      "autoscaling:DescribeTags",
    ]
    resources = [
      "*",
    ]
  }
  statement {
    effect = "Allow"
    actions = [
      "autoscaling:CompleteLifecycleAction",
    ]
    resources = module.eks_cluster.workers_asg_arns
  }
  statement {
    effect = "Allow"
    actions = [
      "sqs:DeleteMessage",
      "sqs:ReceiveMessage"
    ]
    resources = [
      module.aws_node_termination_handler_sqs.sqs_queue_arn
    ]
  }
}
resource "aws_iam_policy" "aws_node_termination_handler" {
  name   = "${local.identifier}-aws-node-termination-handler"
  policy = data.aws_iam_policy_document.aws_node_termination_handler.json
}

data "aws_iam_policy_document" "aws_node_termination_handler_events" {
  statement {
    effect = "Allow"
    principals {
      type = "Service"
      identifiers = [
        "events.amazonaws.com",
        "sqs.amazonaws.com",
      ]
    }
    actions = [
      "sqs:SendMessage",
    ]
    resources = [
      "arn:${data.aws_partition.current.partition}:sqs:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:${local.identifier}",
    ]
  }
}


module "aws_node_termination_handler_sqs" {
  source                    = "terraform-aws-modules/sqs/aws"
  version                   = "~> 3.1.0"
  name                      = local.identifier
  message_retention_seconds = 300
  policy                    = data.aws_iam_policy_document.aws_node_termination_handler_events.json
}

resource "aws_cloudwatch_event_rule" "aws_node_termination_handler_asg" {
  name        = "${local.identifier}-asg-termination"
  description = "Node termination event rule"
  event_pattern = jsonencode(
    {
      "source" : [
        "aws.autoscaling"
      ],
      "detail-type" : [
        "EC2 Instance-terminate Lifecycle Action"
      ]
      "resources" : module.eks_cluster.workers_asg_arns
    }
  )
}

resource "aws_cloudwatch_event_target" "aws_node_termination_handler_asg" {
  target_id = "${local.identifier}-asg-termination"
  rule      = aws_cloudwatch_event_rule.aws_node_termination_handler_asg.name
  arn       = module.aws_node_termination_handler_sqs.sqs_queue_arn
}

resource "aws_cloudwatch_event_rule" "aws_node_termination_handler_spot" {
  name        = "${local.identifier}-spot-termination"
  description = "Node termination event rule"
  event_pattern = jsonencode(
    {
      "source" : [
        "aws.ec2"
      ],
      "detail-type" : [
        "EC2 Spot Instance Interruption Warning"
      ]
      "resources" : module.eks_cluster.workers_asg_arns
    }
  )
}

resource "aws_cloudwatch_event_target" "aws_node_termination_handler_spot" {
  target_id = "${local.identifier}-spot-termination"
  rule      = aws_cloudwatch_event_rule.aws_node_termination_handler_spot.name
  arn       = module.aws_node_termination_handler_sqs.sqs_queue_arn
}

module "aws_node_termination_handler_role" {
  source                        = "terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc"
  version                       = "4.3.0"
  create_role                   = true
  role_description              = "IRSA role for ANTH, cluster ${local.identifier}"
  role_name_prefix              = local.identifier
  provider_url                  = replace(module.eks_cluster.cluster_oidc_issuer_url, "https://", "")
  role_policy_arns              = [aws_iam_policy.aws_node_termination_handler.arn]
  oidc_fully_qualified_subjects = ["system:serviceaccount:kube-system:aws-node-termination-handler"]
}

resource "helm_release" "aws_node_termination_handler" {
  depends_on = [
    module.eks_cluster
  ]

  name             = "aws-node-termination-handler"
  namespace        = "kube-system"
  repository       = "https://aws.github.io/eks-charts"
  chart            = "aws-node-termination-handler"
  version          = "0.15.3"
  create_namespace = true

  set {
    name  = "awsRegion"
    value = data.aws_region.current.name
  }
  set {
    name  = "serviceAccount.name"
    value = "aws-node-termination-handler"
  }
  set {
    name  = "serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn"
    value = module.aws_node_termination_handler_role.iam_role_arn
    type  = "string"
  }
  set {
    name  = "enableSqsTerminationDraining"
    value = "true"
  }
  set {
    name  = "enableSpotInterruptionDraining"
    value = "true"
  }
  set {
    name  = "queueURL"
    value = module.aws_node_termination_handler_sqs.sqs_queue_id
  }
  set {
    name  = "logLevel"
    value = "debug"
  }
}

# Creating the lifecycle-hook outside of the ASG resource's `initial_lifecycle_hook`
# ensures that node termination does not require the lifecycle action to be completed,
# and thus allows the ASG to be destroyed cleanly.
resource "aws_autoscaling_lifecycle_hook" "aws_node_termination_handler" {
  count                  = length(module.eks_cluster.workers_asg_names)
  name                   = "aws-node-termination-handler"
  autoscaling_group_name = module.eks_cluster.workers_asg_names[count.index]
  lifecycle_transition   = "autoscaling:EC2_INSTANCE_TERMINATING"
  heartbeat_timeout      = 300
  default_result         = "CONTINUE"
}