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
    resources = module.eks_cluster.cluster_security_group_arn
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
  version                   = "~> 4.0.2"
  name                      = local.identifier
  message_retention_seconds = 300
  sqs_managed_sse_enabled   = true

  create_queue_policy = true
  queue_policy_statements = {
    account = {
      sid = "ServiceWrite"
      actions = [
        "sqs:SendMessage"
      ]
      principals = [
        {
          type        = "Service"
          identifiers = ["sqs.amazonaws.com", "events.amazonaws.com"]
        }
      ]
    }
  }
}

resource "aws_cloudwatch_event_rule" "k8s_asg_term_rule" {
  name = "${local.identifier}-asg-termination-rule"
  event_pattern = jsonencode({
    "source" : ["aws.autoscaling"],
    "detail-type" : ["EC2 Instance-terminate Lifecycle Action"]
  })
}

resource "aws_cloudwatch_event_target" "k8s_asg_term_rule_target" {
  rule      = aws_cloudwatch_event_rule.k8s_asg_term_rule.name
  target_id = "1"
  arn       = module.aws_node_termination_handler_sqs.queue_arn
}

resource "aws_cloudwatch_event_rule" "k8s_spot_term_rule" {
  name = "${local.identifier}-spot-termination-rule"
  event_pattern = jsonencode({
    "source" : ["aws.ec2"],
    "detail-type" : ["EC2 Spot Instance Interruption Warning"]
  })
}

resource "aws_cloudwatch_event_target" "k8s_spot_term_rule_target" {
  rule      = aws_cloudwatch_event_rule.k8s_spot_term_rule.name
  target_id = "1"
  arn       = module.aws_node_termination_handler_sqs.queue_arn
}

# The following CloudWatch event rules have been temporarily disabled due to an issue where the Node Termination Handler (NTH)
# would cordon nodes but not proceed with workload execution for extended periods. These resources will be re-enabled upon
# resolving the underlying issue. They remain as comments for reference.
#
#resource "aws_cloudwatch_event_rule" "k8s_rebalance_rule" {
#  name        = "${local.identifier}-rebalance-termination-rule"
#  event_pattern = jsonencode({
#    "source" : ["aws.ec2"],
#    "detail-type" : ["EC2 Instance Rebalance Recommendation"]
#  })
#}
#
#resource "aws_cloudwatch_event_target" "k8s_rebalance_rule_target" {
#  rule      = aws_cloudwatch_event_rule.k8s_rebalance_rule.name
#  target_id = "1"
#  arn       = module.aws_node_termination_handler_sqs.queue_arn
#}

resource "aws_cloudwatch_event_rule" "k8s_instance_state_change_rule" {
  name = "${local.identifier}-instance-state-termination-rule"
  event_pattern = jsonencode({
    "source" : ["aws.ec2"],
    "detail-type" : ["EC2 Instance State-change Notification"]
  })
}

resource "aws_cloudwatch_event_target" "k8s_instance_state_change_rule_target" {
  rule      = aws_cloudwatch_event_rule.k8s_instance_state_change_rule.name
  target_id = "1"
  arn       = module.aws_node_termination_handler_sqs.queue_arn
}

resource "aws_cloudwatch_event_rule" "k8s_scheduled_change_rule" {
  name = "${local.identifier}-scheduled-change-termination-rule"
  event_pattern = jsonencode({
    "source" : ["aws.health"],
    "detail-type" : ["AWS Health Event"],
    "detail" : {
      "service" : ["EC2"],
      "eventTypeCategory" : ["scheduledChange"]
    }
  })
}

resource "aws_cloudwatch_event_target" "k8s_scheduled_change_rule_target" {
  rule      = aws_cloudwatch_event_rule.k8s_scheduled_change_rule.name
  target_id = "1"
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