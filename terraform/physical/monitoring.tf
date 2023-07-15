data "aws_iam_policy_document" "lambda_execution" {
  count = var.slack_webhook_url != "" || local.dms_enabled ? 1 : 0

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
  count = var.slack_webhook_url != "" || local.dms_enabled ? 1 : 0

  statement {
    actions = [
      "iam:ListAccountAliases",
      "dms:DescribeReplicationTasks",
      "dms:StartReplicationTask"
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "lambda_permissions" {
  count = var.slack_webhook_url != "" || local.dms_enabled ? 1 : 0

  name   = "${local.identifier}-${data.aws_region.current.name}-lambda-alias"
  policy = data.aws_iam_policy_document.lambda_permissions[0].json
}

resource "aws_iam_role" "lambda_execution" {
  count = var.slack_webhook_url != "" || local.dms_enabled ? 1 : 0

  name               = "${local.identifier}-${data.aws_region.current.name}-lambda-execution"
  assume_role_policy = data.aws_iam_policy_document.lambda_execution[0].json
}
resource "aws_iam_role_policy_attachment" "lambda_basic_execution" {
  count = var.slack_webhook_url != "" || local.dms_enabled ? 1 : 0

  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
  role       = aws_iam_role.lambda_execution[0].name
}
resource "aws_iam_role_policy_attachment" "lambda_iam_alias" {
  count = var.slack_webhook_url != "" || local.dms_enabled ? 1 : 0

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

module "rds_cpu_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

  alarm_name        = "${local.identifier}-rds-cpu-usage"
  alarm_description = "CPU usage for RDS instance ${local.identifier}"

  namespace   = "AWS/RDS"
  metric_name = "CPUUtilization"
  statistic   = "Average"

  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 2
  threshold           = 70
  period              = 300

  dimensions = {
    DBInstanceIdentifier = module.primary_database.db_instance_id
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

module "rds_free_memory_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

  alarm_name        = "${local.identifier}-rds-free-memory"
  alarm_description = "Freeable Memory for RDS instance ${local.identifier}"
  actions_enabled   = true

  alarm_actions             = [module.sns.topic_arn]
  ok_actions                = [module.sns.topic_arn]
  insufficient_data_actions = [module.sns.topic_arn]

  comparison_operator = "LessThanOrEqualToThreshold"
  evaluation_periods  = "2"
  threshold           = local.rds_instance_memory[data.aws_rds_orderable_db_instance.default.instance_class] * 0.20
  unit                = "Bytes"

  datapoints_to_alarm = "2"
  treat_missing_data  = "missing"

  metric_name = "FreeableMemory"
  namespace   = "AWS/RDS"
  period      = "300"
  statistic   = "Average"

  dimensions = {
    DBInstanceIdentifier = module.primary_database.db_instance_id
  }

  tags = {
    Name = "${local.identifier}-rds-free-memory"
  }
}

module "rds_swap_usage_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

  alarm_name        = "${local.identifier}-rds-swap-usage"
  alarm_description = "Swap Usage for RDS instance ${local.identifier}"

  namespace   = "AWS/RDS"
  metric_name = "SwapUsage"
  statistic   = "Average"

  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = "2"
  threshold           = "1000000000" # 1gb
  period              = "300"

  dimensions = {
    DBInstanceIdentifier = module.primary_database.db_instance_id
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

resource "aws_cloudwatch_metric_alarm" "rds_storage_space_alarm" {
  alarm_name          = "${local.identifier}-rds-storage-space"
  alarm_description   = "Storage space usage for RDS instance ${local.identifier}"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = "2"
  threshold           = 0.8 # 80% of used storage space
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]

  metric_query {
    id          = "e1"
    expression  = "1 - m1 / ${var.rds_max_allocated_storage} * 1.0e+9"
    label       = "Storage space used"
    return_data = true
  }

  metric_query {
    id = "m1"
    metric {
      metric_name = "FreeStorageSpace"
      namespace   = "AWS/RDS"
      period      = "300"
      stat        = "Average"
      dimensions = {
        DBInstanceIdentifier = module.primary_database.db_instance_id
      }
    }
  }
}

module "rds_connections_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

  alarm_name        = "${local.identifier}-rds-connections"
  alarm_description = "Connection count for RDS instance ${local.identifier}"

  namespace   = "AWS/RDS"
  metric_name = "DatabaseConnections"
  statistic   = "Average"

  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 2
  threshold           = 250
  period              = 60

  dimensions = {
    DBInstanceIdentifier = module.primary_database.db_instance_id
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

module "rds_read_latency_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

  alarm_name          = "${local.identifier}-rds-read-latency"
  alarm_description   = "Read latency for RDS instance ${local.identifier}"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 0.1 # Change as per your requirements, measured in seconds
  evaluation_periods  = "2"
  period              = "300"

  namespace   = "AWS/RDS"
  metric_name = "ReadLatency"
  statistic   = "Average"

  dimensions = {
    DBInstanceIdentifier = module.primary_database.db_instance_id
  }

  alarm_actions = [module.sns.topic_arn]
  ok_actions    = [module.sns.topic_arn]
}

module "rds_write_latency_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "4.2.1"

  alarm_name          = "${local.identifier}-rds-write-latency"
  alarm_description   = "Write latency for RDS instance ${local.identifier}"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 0.1 # Change as per your requirements, measured in seconds
  evaluation_periods  = "2"
  period              = "300"

  namespace   = "AWS/RDS"
  metric_name = "WriteLatency"
  statistic   = "Average"

  dimensions = {
    DBInstanceIdentifier = module.primary_database.db_instance_id
  }

  alarm_actions = [module.sns.topic_arn]
  ok_actions    = [module.sns.topic_arn]
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

// -- Slack Notification Lambda

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
      AWS_ACCOUNT_ID    = data.aws_caller_identity.current.account_id
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

// -- DMS Restart Lambda

data "archive_file" "dms_restart_lambda" {
  count = local.dms_enabled ? 1 : 0

  type        = "zip"
  source_file = "util/dms_restart.py"
  output_path = "dms_restart_lambda_payload.zip"
}

resource "aws_lambda_function" "dms_restart" {
  count = local.dms_enabled ? 1 : 0

  filename      = "dms_restart_lambda_payload.zip"
  function_name = "${local.identifier}-dms_restart"
  handler       = "dms_restart.lambda_handler"
  runtime       = "python3.8"
  role          = aws_iam_role.lambda_execution[0].arn

  source_code_hash = data.archive_file.dms_restart_lambda[0].output_base64sha256

}

resource "aws_sns_topic_subscription" "dms_restart_subscription" {
  count = local.dms_enabled ? 1 : 0

  topic_arn = module.sns.topic_arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.dms_restart[0].arn
}

resource "aws_lambda_permission" "dms_restart_permission" {
  count = local.dms_enabled ? 1 : 0

  statement_id  = "AllowSNSInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.dms_restart[0].function_name
  principal     = "sns.amazonaws.com"
  source_arn    = module.sns.topic_arn
}