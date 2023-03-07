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
      module.aws_node_termination_handler_sqs.queue_arn
    ]
  }
}
resource "aws_iam_policy" "aws_node_termination_handler" {
  name   = "${local.identifier}-${data.aws_region.current.name}-aws-node-termination-handler"
  policy = data.aws_iam_policy_document.aws_node_termination_handler.json
}

module "aws_node_termination_handler_sqs" {
  source                    = "terraform-aws-modules/sqs/aws"
  version                   = "~> 4.0.1"
  name                      = local.identifier
  message_retention_seconds = 300
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
  arn       = module.aws_node_termination_handler_sqs.queue_arn
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
  arn       = module.aws_node_termination_handler_sqs.queue_arn
}

module "aws_node_termination_handler_role" {
  source                        = "terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc"
  version                       = "5.11.2"
  create_role                   = true
  role_description              = "IRSA role for ANTH, cluster ${local.identifier}"
  role_name_prefix              = local.identifier
  provider_url                  = replace(module.eks_cluster.cluster_oidc_issuer_url, "https://", "")
  role_policy_arns              = [aws_iam_policy.aws_node_termination_handler.arn]
  oidc_fully_qualified_subjects = ["system:serviceaccount:kube-system:aws-node-termination-handler"]
}