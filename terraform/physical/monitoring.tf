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

  name   = "${local.identifier}-${data.aws_region.current.id}-lambda-alias"
  policy = data.aws_iam_policy_document.lambda_permissions[0].json
}

resource "aws_iam_role" "lambda_execution" {
  count = var.slack_webhook_url != "" || local.dms_enabled ? 1 : 0

  name               = "${local.identifier}-${data.aws_region.current.id}-lambda-execution"
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
  version = "~> 7.0"
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

module "node_cpu_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "~> 5.0"

  alarm_name        = "${local.identifier}-cpu-high"
  alarm_description = "CPU utilization high for ${local.identifier} cluster"

  namespace   = "ContainerInsights"
  metric_name = "node_cpu_utilization"
  statistic   = "Average"

  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  threshold           = 90
  period              = 60

  dimensions = {
    ClusterName = module.eks_cluster.cluster_name
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
  version = "~> 5.0"

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
    ClusterName = module.eks_cluster.cluster_name
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
  version = "~> 5.0"

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
    ClusterName = module.eks_cluster.cluster_name
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

# RDS alarm dimensions use local.identifier (the value passed to the rds module's
# `identifier`), NOT module.primary_database.db_instance_id. The rds module is pinned
# to v5.6.0, whose db_instance_id output returns aws_db_instance.this.id — and under
# AWS provider v5+ that .id is the resource ID (db-XXXX), not the instance identifier.
# CloudWatch's AWS/RDS namespace keys on the identifier, so the resource ID matches no
# metric and every RDS alarm sits in INSUFFICIENT_DATA. local.identifier is correct by
# construction and immune to provider/module-version drift.
module "rds_cpu_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "~> 5.0"

  create_metric_alarm = var.db_engine == "rds"

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
    DBInstanceIdentifier = local.identifier
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
  version = "~> 5.0"

  create_metric_alarm = var.db_engine == "rds"

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
    DBInstanceIdentifier = local.identifier
  }

  tags = {
    Name = "${local.identifier}-rds-free-memory"
  }
}

module "rds_swap_usage_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "~> 5.0"

  create_metric_alarm = var.db_engine == "rds"

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
    DBInstanceIdentifier = local.identifier
  }

  alarm_actions = [
    module.sns.topic_arn
  ]

  ok_actions = [
    module.sns.topic_arn
  ]
}

resource "aws_cloudwatch_metric_alarm" "rds_storage_space_alarm" {
  count = var.db_engine == "rds" ? 1 : 0

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
        DBInstanceIdentifier = local.identifier
      }
    }
  }
}

module "rds_connections_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "~> 5.0"

  create_metric_alarm = var.db_engine == "rds"

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
    DBInstanceIdentifier = local.identifier
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
  version = "~> 5.0"

  create_metric_alarm = var.db_engine == "rds"

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
    DBInstanceIdentifier = local.identifier
  }

  alarm_actions = [module.sns.topic_arn]
  ok_actions    = [module.sns.topic_arn]
}

module "rds_write_latency_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "~> 5.0"

  create_metric_alarm = var.db_engine == "rds"

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
    DBInstanceIdentifier = local.identifier
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

# Content-based archive: zips the file's BYTES (not the on-disk file with its
# metadata), so the zip is byte-identical on every machine/checkout, at plan and
# apply. Paths anchored to path.module so they don't depend on the working dir.
data "archive_file" "slack_sns_lambda" {
  count = var.slack_webhook_url != "" ? 1 : 0

  type        = "zip"
  output_path = "${path.module}/sns_lambda_payload.zip"

  source {
    content  = file("${path.module}/util/sns_to_slack.py")
    filename = "sns_to_slack.py"
  }
}

resource "aws_lambda_function" "sns_to_slack" {
  count = var.slack_webhook_url != "" ? 1 : 0

  filename      = data.archive_file.slack_sns_lambda[0].output_path
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
  output_path = "${path.module}/dms_restart_lambda_payload.zip"

  source {
    content  = file("${path.module}/util/dms_restart.py")
    filename = "dms_restart.py"
  }
}

resource "aws_lambda_function" "dms_restart" {
  count = local.dms_enabled ? 1 : 0

  filename      = data.archive_file.dms_restart_lambda[0].output_path
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

# --- DR replication health (count-gated on enable_dr) --- #

# S3 CRR: pending bytes growing unboundedly means replication is failing/stuck.
resource "aws_cloudwatch_metric_alarm" "dr_s3_replication_latency" {
  for_each = var.enable_dr ? aws_s3_bucket.guide_buckets : {}

  alarm_name          = "${local.identifier}-dr-s3-replication-${each.key}"
  alarm_description   = "S3 DR replication latency high for ${local.identifier} ${each.key} bucket"
  namespace           = "AWS/S3"
  metric_name         = "ReplicationLatency"
  statistic           = "Maximum"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 900
  evaluation_periods  = 3
  period              = 300
  treat_missing_data  = "notBreaching"

  dimensions = {
    SourceBucket      = each.value.id
    DestinationBucket = aws_s3_bucket.dr_guide_buckets[each.key].id
    RuleId            = "dr-${each.key}"
  }

  alarm_actions = [module.sns.topic_arn]
  ok_actions    = [module.sns.topic_arn]
}

resource "aws_cloudwatch_metric_alarm" "dr_s3_replication_failed" {
  for_each = var.enable_dr ? aws_s3_bucket.guide_buckets : {}

  alarm_name          = "${local.identifier}-dr-s3-replication-failed-${each.key}"
  alarm_description   = "S3 DR replication operations failing for ${local.identifier} ${each.key} bucket"
  namespace           = "AWS/S3"
  metric_name         = "OperationsFailedReplication"
  statistic           = "Sum"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  evaluation_periods  = 1
  period              = 300
  treat_missing_data  = "notBreaching"

  dimensions = {
    SourceBucket      = each.value.id
    DestinationBucket = aws_s3_bucket.dr_guide_buckets[each.key].id
    RuleId            = "dr-${each.key}"
  }

  alarm_actions = [module.sns.topic_arn]
  ok_actions    = [module.sns.topic_arn]
}

# --- Aurora CloudWatch alarms (count-gated on aurora engine) --- #

resource "aws_cloudwatch_metric_alarm" "aurora_cpu" {
  count               = var.db_engine == "aurora" ? 1 : 0
  alarm_name          = "${local.identifier}-aurora-cpu"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  metric_name         = "CPUUtilization"
  namespace           = "AWS/RDS"
  period              = 300
  statistic           = "Average"
  threshold           = 85
  alarm_description   = "Aurora cluster CPU high"
  dimensions          = { DBClusterIdentifier = local.identifier }
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]
  tags                = local.tags
}

resource "aws_cloudwatch_metric_alarm" "aurora_memory" {
  count               = var.db_engine == "aurora" ? 1 : 0
  alarm_name          = "${local.identifier}-aurora-freeable-memory"
  comparison_operator = "LessThanThreshold"
  evaluation_periods  = 3
  metric_name         = "FreeableMemory"
  namespace           = "AWS/RDS"
  period              = 300
  statistic           = "Average"
  threshold           = 536870912 # 512 MiB
  alarm_description   = "Aurora cluster freeable memory low"
  dimensions          = { DBClusterIdentifier = local.identifier }
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]
  tags                = local.tags
}

resource "aws_cloudwatch_metric_alarm" "aurora_connections" {
  count               = var.db_engine == "aurora" ? 1 : 0
  alarm_name          = "${local.identifier}-aurora-connections"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  metric_name         = "DatabaseConnections"
  namespace           = "AWS/RDS"
  period              = 300
  statistic           = "Average"
  threshold           = 1000
  alarm_description   = "Aurora cluster connection count high"
  dimensions          = { DBClusterIdentifier = local.identifier }
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]
  tags                = local.tags
}

resource "aws_cloudwatch_metric_alarm" "aurora_acu" {
  count               = var.db_engine == "aurora" ? 1 : 0
  alarm_name          = "${local.identifier}-aurora-acu-ceiling"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 3
  metric_name         = "ACUUtilization"
  namespace           = "AWS/RDS"
  period              = 300
  statistic           = "Average"
  threshold           = 90
  alarm_description   = "Aurora Serverless v2 capacity near max ACU"
  dimensions          = { DBClusterIdentifier = local.identifier }
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]
  tags                = local.tags
}
