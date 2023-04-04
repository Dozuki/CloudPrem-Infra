data "aws_iam_policy_document" "lambda_execution" {
  count = var.slack_webhook_url != "" ? 1 : 0

  statement {
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["lambda.${data.aws_partition.current.dns_suffix}"]
    }

    actions = ["sts:AssumeRole"]
  }
}

data "aws_iam_policy_document" "lambda_permissions" {
  count = var.slack_webhook_url != "" ? 1 : 0

  statement {
    actions = [
      "iam:ListAccountAliases",
      "dms:DescribeReplicationTasks"
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "lambda_permissions" {
  count = var.slack_webhook_url != "" ? 1 : 0

  name   = "${local.identifier}-${data.aws_region.current.name}-lambda-alias"
  policy = data.aws_iam_policy_document.lambda_permissions[0].json
}

resource "aws_iam_role" "lambda_execution" {
  count = var.slack_webhook_url != "" ? 1 : 0

  name               = "${local.identifier}-${data.aws_region.current.name}-lambda-execution"
  assume_role_policy = data.aws_iam_policy_document.lambda_execution[0].json
}
resource "aws_iam_role_policy_attachment" "lambda_basic_execution" {
  count = var.slack_webhook_url != "" ? 1 : 0

  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
  role       = aws_iam_role.lambda_execution[0].name
}
resource "aws_iam_role_policy_attachment" "lambda_iam_alias" {
  count = var.slack_webhook_url != "" ? 1 : 0

  policy_arn = aws_iam_policy.lambda_permissions[0].arn
  role       = aws_iam_role.lambda_execution[0].name
}

module "sns" {
  source  = "terraform-aws-modules/sns/aws"
  version = "5.1.0"
  name    = local.identifier

  topic_policy_statements = {
    pub = {
      actions = ["sns:Publish"]
      principals = [{
        type        = "Service"
        identifiers = ["events.${data.aws_partition.current.dns_suffix}", "dms.${data.aws_partition.current.dns_suffix}"]
      }]
    }
  }
}

resource "aws_sns_topic_subscription" "email_subscription" {
  count = var.alarm_email != "" ? 1 : 0

  topic_arn = module.sns.topic_arn
  protocol  = "email"
  endpoint  = var.alarm_email # Replace with your email address
}

# The alarm should never trigger unless something is wrong with the cluster autoscaler, or the max scale has been met
module "cpu_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

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
    AutoScalingGroupName = module.eks_cluster.workers_asg_names[0]
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

# The alarm should never trigger unless something is wrong with the cluster autoscaler, or the max scale has been met
module "memory_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

  alarm_name        = "${local.identifier}-memory-utilization"
  alarm_description = "High memory utilization for ${local.identifier} cluster"

  namespace   = "ContainerInsights"
  metric_name = "node_memory_utilization"
  statistic   = "Average"

  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  threshold           = 80
  period              = 300

  dimensions = {
    ClusterName = module.eks_cluster.cluster_id
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

module "disk_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

  alarm_name        = "${local.identifier}-out-of-disk"
  alarm_description = "Disk usage high for ${local.identifier} cluster"

  namespace   = "ContainerInsights"
  metric_name = "node_filesystem_utilization"
  statistic   = "Average"

  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  threshold           = 60
  period              = 300

  dimensions = {
    ClusterName = module.eks_cluster.cluster_id
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

module "status_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

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
    AutoScalingGroupName = module.eks_cluster.workers_asg_names[0]
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

module "nodes_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

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
    AutoScalingGroupName = module.eks_cluster.workers_asg_names[0]
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

resource "aws_cloudwatch_event_rule" "dms_task_state_changed_rule" {
  count = var.enable_bi ? 1 : 0

  name        = "${local.identifier}-dms-task-changed-rule"
  description = "Capture change state of DMS replication tasks"

  event_pattern = jsonencode({
    "source" : [
      "aws.dms"
    ],
    "detail-type" : [
      "DMS Replication Task State Change"
    ]
  })
}

resource "aws_cloudwatch_event_target" "dms_task_state_changed_target" {
  count = var.enable_bi ? 1 : 0

  rule      = aws_cloudwatch_event_rule.dms_task_state_changed_rule[0].name
  target_id = "DmsTaskChangedTarget"
  arn       = module.sns.topic_arn
}


data "archive_file" "slack_sns_lambda" {
  count = var.slack_webhook_url != "" ? 1 : 0

  type        = "zip"
  source_file = "util/sns_to_slack.py"
  output_path = "sns_lambda_payload.zip"
}

resource "aws_lambda_function" "sns_to_slack" {
  count = var.slack_webhook_url != "" ? 1 : 0

  filename      = "sns_lambda_payload.zip"
  function_name = "${local.identifier}-sns_to_slack"
  handler       = "sns_to_slack.lambda_handler"
  runtime       = "python3.8"
  role          = aws_iam_role.lambda_execution[0].arn

  source_code_hash = data.archive_file.slack_sns_lambda[0].output_base64sha256

  environment {
    variables = {
      SLACK_WEBHOOK_URL = var.slack_webhook_url
      IDENTIFIER        = local.identifier
    }
  }
}

resource "aws_sns_topic_subscription" "sns_to_slack_subscription" {
  count = var.slack_webhook_url != "" ? 1 : 0

  topic_arn = module.sns.topic_arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.sns_to_slack[0].arn
}

resource "aws_lambda_permission" "sns_to_slack_permission" {
  count = var.slack_webhook_url != "" ? 1 : 0

  statement_id  = "AllowSNSInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.sns_to_slack[0].function_name
  principal     = "sns.amazonaws.com"
  source_arn    = module.sns.topic_arn
}