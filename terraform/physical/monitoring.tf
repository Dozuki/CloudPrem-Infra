locals {
  # Slack delivery works over either transport: the legacy incoming webhook, or
  # the bot token + channel pair. Prefer the token: webhook posts have a
  # synthetic author no token can delete or edit, and the webhook URL sits in
  # the lambda's plaintext env; the token lives in an SSM SecureString the
  # lambda reads at runtime. Every token-path resource gates on slack_use_token
  # (the complete pair), never on token presence alone - a half-set pair is
  # also rejected by the slack_bot_token variable validation.
  slack_use_token             = var.slack_bot_token != "" && var.slack_channel_id != ""
  slack_notifications_enabled = var.slack_webhook_url != "" || local.slack_use_token
}

data "aws_iam_policy_document" "lambda_execution" {
  count = local.slack_notifications_enabled || local.dms_enabled ? 1 : 0

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
  count = local.slack_notifications_enabled || local.dms_enabled ? 1 : 0

  statement {
    actions = [
      "iam:ListAccountAliases",
      # Serverless replications and provisioned tasks are separate API surfaces with
      # separate IAM actions, and this lambda now handles both: the BI replication is a
      # replication-config, the aurora migration is still a task. Granting only the task
      # actions made every serverless restart fail AccessDenied - and because the lambda
      # is fired by an event rather than an apply, that failure is invisible until you go
      # looking for the restart that never happened.
      "dms:DescribeReplicationTasks",
      "dms:StartReplicationTask",
      "dms:StartReplication",
      # get_task_name resolves a replication-config ARN to its identifier through this. Without
      # it the call returns AccessDenied, which that function deliberately swallows so an alert
      # still goes out - meaning the only symptom is every serverless alert naming an opaque
      # ARN instead of the replication, with nothing in the logs anyone reads.
      "dms:DescribeReplicationConfigs",
      # dms_restart.py's restart cooldown reads its own log group back for the AUTO-RESTART
      # marker (stateless flap-breaker; usac 2026-08-03 restarted a CPU-walled full load in
      # a loop). The check fails open, so missing this grant looks like the cooldown simply
      # not working - restarts loop again - not like an error anyone gets paged for.
      "logs:FilterLogEvents"
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "lambda_permissions" {
  count = local.slack_notifications_enabled || local.dms_enabled ? 1 : 0

  name   = "${local.identifier}-${data.aws_region.current.region}-lambda-alias"
  policy = data.aws_iam_policy_document.lambda_permissions[0].json

  tags = local.tags
}

resource "aws_iam_role" "lambda_execution" {
  count = local.slack_notifications_enabled || local.dms_enabled ? 1 : 0

  name               = "${local.identifier}-${data.aws_region.current.region}-lambda-execution"
  assume_role_policy = data.aws_iam_policy_document.lambda_execution[0].json

  tags = local.tags
}
resource "aws_iam_role_policy_attachment" "lambda_basic_execution" {
  count = local.slack_notifications_enabled || local.dms_enabled ? 1 : 0

  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
  role       = aws_iam_role.lambda_execution[0].name
}
resource "aws_iam_role_policy_attachment" "lambda_iam_alias" {
  count = local.slack_notifications_enabled || local.dms_enabled ? 1 : 0

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
  endpoint  = var.alarm_email
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

  tags = local.tags
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
  threshold           = 0.1 # seconds
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
  threshold           = 0.1 # seconds
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
  # Present whenever ANY dms task this env owns exists: the BI task OR the
  # aurora migration task. Gating on enable_bi alone left a migration on an
  # enable_bi=false env alertless - with the STOP_TASK apply-error policies a
  # soak-phase stop must page, or it sits unnoticed past binlog retention.
  # Keyed on dms_enabled, not enable_bi: pure-replication BI (enable_bi with
  # bi_dms_enabled/bi_public_access false) owns no DMS resources, so the
  # resources pattern below renders [] and EventBridge rejects the rule
  # outright (InvalidEventPatternException: Empty arrays are not allowed).
  count = local.dms_enabled || local.aurora_migration_dms ? 1 : 0

  name        = "${local.identifier}-dms-task-changed-rule"
  description = "Capture change state of DMS replications (BI serverless) and tasks (aurora migration)"

  # Scoped to THIS env's task ARNs: DMS state-change events are account+region
  # wide, so an unscoped pattern forwards every other env's task events too -
  # each env's slack lambda then stamps its own identifier on a foreign task
  # (an m3-apac migration OOM alerted as m3-gca).
  event_pattern = jsonencode({
    "source" : [
      "aws.dms"
    ],
    # detail-type is deliberately NOT pinned. The BI replication is serverless (a
    # Replication, not a ReplicationTask) and the aurora migration is still a task, so the two
    # emit different detail-types. Matching on source + this env's resource ARNs catches both
    # and cannot silently stop matching if AWS words the serverless detail-type differently
    # than we assumed - a rule that never fires is indistinguishable from a healthy one.
    "resources" : compact(concat(
      [for r in aws_dms_replication_config.this : r.arn],
      local.aurora_migration_dms ? [aws_dms_replication_task.aurora_migration[0].replication_task_arn] : []
    ))
  })

  tags = local.tags
}

resource "aws_cloudwatch_event_target" "dms_task_state_changed_target" {
  # Same gate as the rule above; must never exist without it.
  count = local.dms_enabled || local.aurora_migration_dms ? 1 : 0

  rule      = aws_cloudwatch_event_rule.dms_task_state_changed_rule[0].name
  target_id = "DmsTaskChangedTarget"
  arn       = module.sns.topic_arn
}

# --- BI serverless CDC latency alarms --- #
#
# Serverless replications do NOT publish under the provisioned-task dimensions
# (ReplicationInstanceIdentifier + ReplicationTaskIdentifier); they publish under
# a single ReplicationConfigId dimension whose value is "<account>:<config id>".
# The config id changes every time the replication config is replaced (endpoint
# rename, compute change), so the dimension is derived from the config ARN here
# and follows any replacement in the same apply. Hand-created alarms keyed to a
# task id latch into ALARM forever the moment the task is deleted.
#
# missing=breaching is deliberate: a deprovisioned or stuck replication emits
# nothing, and it must page before the source's binlog retention window makes a
# resume impossible. A new metric identity - greenfield env, or any config
# replacement rotating the dimension value - has no datapoints, so every
# lookback slot counts as breaching and the alarm pages at its first
# evaluation, staying in ALARM until the logical layer starts the replication.
# That page is accurate (BI is not replicating) and expected during those
# windows; do not "fix" it by switching to notBreaching, which trades it for
# silent BI death.
#
# datapoints_to_alarm is deliberately BELOW evaluation_periods, and that gap is
# the whole point - do not "simplify" it back to 9/9 or 12/12. CloudWatch sizes
# the two directions from the same pair, and it is not symmetric:
#
#   OK    -> ALARM   needs datapoints_to_alarm                 = 9 breaching
#   ALARM -> OK      needs evaluation_periods - dta + 1        = 4 non-breaching
#
# At 6/6 the resolve side collapsed to ONE non-breaching datapoint, which made
# these two alarms flap hard around every stop/start of the replication. DMS
# publishes CDCLatency* sparsely while CDC is spinning up (observed: a single
# 5-min bucket, then a 70-minute gap), and with missing=breaching a lone
# datapoint was enough to drive ALARM->OK, then its ageing out drove OK->ALARM
# again ~30min later. One planned stop/start produced 14 notifications across
# the pair, none of which said anything the first page had not.
#
# 9-of-12 fixes the resolve side: clearing now takes 4 non-breaching datapoints
# within the last 12 periods (60min), so one sparse datapoint can no longer do
# it. Note M-of-N does not require consecutive datapoints - 4 healthy readings
# anywhere in the hour will clear it, they do not have to be adjacent. It also
# swallows the restart transient for free - a resumed replication opens with the
# whole accumulated binlog gap as its first reading (observed 11851s, drained to
# single digits in the next period), and that spike now lands while the alarm is
# ALREADY ALARM, so it causes no state transition and pages nobody.
#
# Cost of the wider window: the "replication stopped" page arrives after ~45min
# of silence instead of ~30 (both are floors - CloudWatch keeps re-evaluating
# the last real datapoints for a few periods after a metric stops, which adds an
# unpublished overhang to either config equally). That is immaterial against the
# 24h binlog retention this alarm exists to protect (bi.tf sets 'binlog
# retention hours' 24 on the source endpoints; the 48h in aurora-migration.tf is
# a different feature). A stop that emits a state-change event is separately and
# immediately caught by dms_task_state_changed_rule above, but a replication
# that stalls or goes silent without a state change emits no event - that is the
# case these alarms uniquely cover, and there the extra 15min is real.
locals {
  bi_replication_config_id = local.dms_enabled ? join(":", [
    split(":", aws_dms_replication_config.this[0].arn)[4],
    split(":", aws_dms_replication_config.this[0].arn)[6],
  ]) : ""
}

resource "aws_cloudwatch_metric_alarm" "bi_cdc_latency_source" {
  count               = local.dms_enabled ? 1 : 0
  alarm_name          = "${local.identifier}-dms-cdc-latency-source"
  alarm_description   = "BI serverless replication ${local.identifier}: CDCLatencySource high or not reporting (missing=breaching)"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 900 # seconds
  evaluation_periods  = 12
  datapoints_to_alarm = 9
  period              = 300
  namespace           = "AWS/DMS"
  metric_name         = "CDCLatencySource"
  statistic           = "Maximum"
  treat_missing_data  = "breaching"
  dimensions          = { ReplicationConfigId = local.bi_replication_config_id }
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]
  tags                = local.tags
}

resource "aws_cloudwatch_metric_alarm" "bi_cdc_latency_target" {
  count               = local.dms_enabled ? 1 : 0
  alarm_name          = "${local.identifier}-dms-cdc-latency-target"
  alarm_description   = "BI serverless replication ${local.identifier}: CDCLatencyTarget high or not reporting (missing=breaching)"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 900 # seconds
  evaluation_periods  = 12
  datapoints_to_alarm = 9
  period              = 300
  namespace           = "AWS/DMS"
  metric_name         = "CDCLatencyTarget"
  statistic           = "Maximum"
  treat_missing_data  = "breaching"
  dimensions          = { ReplicationConfigId = local.bi_replication_config_id }
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]
  tags                = local.tags
}

# Capacity saturation. This is the alarm that lets the slack lambda drop the per-event
# scaling chatter: a serverless replication posts every scale up and scale down decision,
# and no single one of those tells you whether the replication is actually short of
# capacity over any span worth acting on. Repeated 90% peaks across an hour do.
#
# AWS's own guidance ties this metric to the failure mode: a min or max DCU set too low
# for the workload shows up as CapacityUtilization "consistently at its maximum value",
# and the replication can fail outright with an out-of-memory event. The fix is raising
# bi_dms_min_dcu (or max), so the alarm is naming a config change, not a transient.
#
# Read the condition precisely: Maximum with 12/12 datapoints means at least one sample
# reached 90% in each of twelve consecutive 5-minute buckets. That is repeated peaking,
# NOT continuous saturation - a replication oscillating between 40% and 91% trips this.
# That is the intended reading (peaks are what precede an OOM), but do not quote this
# alarm as evidence that utilization sat above 90% for an hour; it cannot show that.
# Switch the statistic to Average if the sustained reading is ever what is wanted.
#
# Known blind spot: on the one full load observed here (gca, 2026-07-31 19:18-19:24Z) DMS
# published no CapacityUtilization datapoints at all - the metric did not start until
# 20:45Z. Whether that generalises to every full load is unverified, but assume this alarm
# may be silent during one, which is exactly when utilization peaks. That gap is covered
# elsewhere rather than here: a full-load OOM emits "DMS replication has failed", which
# pages via sns_to_slack.py and triggers the restart lambda.
#
# Bigger blind spot, proven on usac 2026-08-03: a DCU is a memory-denominated unit, and
# this metric tracks memory occupancy of the provision. A CPU-bound replication (20.6k
# small tables, validation on) died FATAL after 25 minutes of CPUUtilization at 89-94%
# while THIS metric read 7-13% the whole time. This alarm cannot see that failure mode
# at any threshold; that is what bi_dms_cpu_saturated below is for.
#
# ignore, not notBreaching, and not breaching. The CDC latency alarms above already own
# "replication stopped reporting" (they are missing=breaching), so a second one here would
# double-page every deprovision and every config replacement. But notBreaching is wrong in
# the other direction: if this alarm is ALREADY in ALARM and the replication then dies and
# stops emitting, notBreaching resolves it ALARM -> OK and posts a recovery to slack for a
# replication that is actually dead. ignore holds the ALARM state instead.
resource "aws_cloudwatch_metric_alarm" "bi_dms_capacity_saturated" {
  count               = local.dms_enabled ? 1 : 0
  alarm_name          = "${local.identifier}-dms-capacity-saturated"
  alarm_description   = "BI serverless replication ${local.identifier}: CapacityUtilization peaked >= 90% in every 5-min bucket for an hour - raise bi_dms_min_dcu/bi_dms_max_dcu before it OOMs"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 90
  # 12x300s = an hour of repeated peaks. Deliberately long: a full load legitimately runs
  # hot, and scale-up is not instant, so a shorter window pages on healthy bursts - the
  # exact noise this alarm exists to replace.
  evaluation_periods  = 12
  datapoints_to_alarm = 12
  period              = 300
  namespace           = "AWS/DMS"
  metric_name         = "CapacityUtilization"
  statistic           = "Maximum"
  treat_missing_data  = "ignore"
  dimensions          = { ReplicationConfigId = local.bi_replication_config_id }
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]
  tags                = local.tags
}

# CPU saturation - the failure mode CapacityUtilization is blind to. usac 2026-08-03:
# the first BI full load at max 32 DCU held CPUUtilization at 89-94% from 15:55Z, the
# autoscaler logged "cannot scale up as the replication is already at the provided
# Maximum" at 16:03Z, and the replication died at ~16:20Z with an empty Last Error
# ("Internal failure"), nothing in the task log, and CapacityUtilization never leaving
# 13%. The restart lambda then reload-targeted it into the same wall (no backoff), so
# the first alarm-worthy signal a human got was the CRITICAL failure page.
#
# Average, not Maximum: a CPU-bound replication pegs continuously (five straight 5-min
# averages of 89-93 observed), it does not oscillate. 3x300s at >= 90 would have fired
# at 16:10Z, ten minutes before the death. Full loads are rare, operator-adjacent
# events; paging ten minutes early on a healthy-but-hot load is the acceptable cost.
# The fix this alarm names is raising bi_dms_max_dcu (memory-shaped stalls belong to
# bi_dms_capacity_saturated above).
#
# treat_missing_data ignore for the same reason as the capacity alarm: the CDC latency
# pair owns "stopped reporting", and notBreaching would post a fake OK for a dead
# replication that had been in ALARM.
#
# This alarm WILL fire during a healthy mid-load scale-up, and that is a considered
# trade, not an oversight (usac 2026-08-03 17:29Z, 25 minutes after the alarm was
# created): the autoscaler itself needs ~15 min of pegged CPU before it reacts, plus
# 5-10 to attach capacity, so a window quiet through scale-ups needs ~30 min - and the
# one observed fatal ran peg-to-death in 25. There is no separating window. Read it as:
# fires then self-resolves = load is hot and the autoscaler engaged; fires and stays
# red = at the cap or the autoscaler is stuck, act. Steady-state CDC runs under 5% CPU
# 92-100% of hours (see bi_dms_min_dcu), so this only pages during full loads or real
# trouble. If self-resolving pages prove too noisy, go to 4/4 periods (20 min), never
# 30 - that reopens the blind window this alarm exists to close.
resource "aws_cloudwatch_metric_alarm" "bi_dms_cpu_saturated" {
  count               = local.dms_enabled ? 1 : 0
  alarm_name          = "${local.identifier}-dms-cpu-saturated"
  alarm_description   = "BI serverless replication ${local.identifier}: CPUUtilization averaged >= 90% for 15 min - CPU-bound at the DCU cap, raise bi_dms_max_dcu before it dies (CapacityUtilization stays low in this mode, do not trust it)"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = 90
  evaluation_periods  = 3
  datapoints_to_alarm = 3
  period              = 300
  namespace           = "AWS/DMS"
  metric_name         = "CPUUtilization"
  statistic           = "Average"
  treat_missing_data  = "ignore"
  dimensions          = { ReplicationConfigId = local.bi_replication_config_id }
  alarm_actions       = [module.sns.topic_arn]
  ok_actions          = [module.sns.topic_arn]
  tags                = local.tags
}

// -- Slack Notification Lambda

# Content-based archive: zips the file's BYTES (not the on-disk file with its
# metadata), so the zip is byte-identical on every machine/checkout, at plan and
# apply. Paths anchored to path.module so they don't depend on the working dir.
data "archive_file" "slack_sns_lambda" {
  count = local.slack_notifications_enabled ? 1 : 0

  type        = "zip"
  output_path = "${path.module}/sns_lambda_payload.zip"

  source {
    content  = file("${path.module}/util/sns_to_slack.py")
    filename = "sns_to_slack.py"
  }
}

resource "aws_lambda_function" "sns_to_slack" {
  count = local.slack_notifications_enabled ? 1 : 0

  filename      = data.archive_file.slack_sns_lambda[0].output_path
  function_name = "${local.identifier}-sns_to_slack"
  handler       = "sns_to_slack.lambda_handler"
  # Same treatment as dms_restart below: arm64 + off the deprecated python3.8.
  runtime       = "python3.12"
  architectures = ["arm64"]
  role          = aws_iam_role.lambda_execution[0].arn

  source_code_hash = data.archive_file.slack_sns_lambda[0].output_base64sha256

  # 3s default is too tight for the token path: an SSM GetParameter plus the
  # Slack POST on a cold start can exceed it. Nothing invokes this
  # synchronously, so be generous.
  timeout = 30

  environment {
    variables = {
      SLACK_WEBHOOK_URL         = var.slack_webhook_url
      SLACK_BOT_TOKEN_SSM_PARAM = local.slack_use_token ? aws_ssm_parameter.slack_bot_token[0].name : ""
      SLACK_CHANNEL_ID          = var.slack_channel_id
      SLACK_STATE_TABLE         = local.slack_use_token ? aws_dynamodb_table.slack_alarm_state[0].name : ""
      IDENTIFIER                = local.identifier
      AWS_ACCOUNT_ID            = data.aws_caller_identity.current.account_id
    }
  }

  tags = local.tags
}

# Correlate CloudWatch ALARM -> OK on the bot-token path so recovery edits the
# original incident card and adds one thread event instead of creating a second,
# disconnected root post. The webhook fallback cannot update messages and does
# not use this table. Resolved rows remain briefly as idempotency tombstones and
# expire automatically.
resource "aws_dynamodb_table" "slack_alarm_state" {
  count = local.slack_use_token ? 1 : 0

  name         = "${local.identifier}-slack-alarm-state"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "AlarmKey"

  attribute {
    name = "AlarmKey"
    type = "S"
  }

  ttl {
    attribute_name = "ExpiresAt"
    enabled        = true
  }

  server_side_encryption {
    enabled = true
  }

  tags = local.tags
}

resource "aws_ssm_parameter" "slack_bot_token" {
  count = local.slack_use_token ? 1 : 0

  name  = "/${local.identifier}/slack-bot-token"
  type  = "SecureString"
  value = var.slack_bot_token

  tags = local.tags
}

resource "aws_iam_role_policy" "sns_to_slack_bot_token" {
  count = local.slack_use_token ? 1 : 0

  name = "${local.identifier}-sns-to-slack-bot-token"
  role = aws_iam_role.lambda_execution[0].name
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect   = "Allow",
      Action   = ["ssm:GetParameter"],
      Resource = aws_ssm_parameter.slack_bot_token[0].arn
    }]
  })
}

resource "aws_iam_role_policy" "sns_to_slack_alarm_state" {
  count = local.slack_use_token ? 1 : 0

  name = "${local.identifier}-sns-to-slack-alarm-state"
  role = aws_iam_role.lambda_execution[0].name
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect = "Allow",
      Action = [
        "dynamodb:GetItem",
        "dynamodb:PutItem",
      ],
      Resource = aws_dynamodb_table.slack_alarm_state[0].arn
    }]
  })
}

resource "aws_sns_topic_subscription" "sns_to_slack_subscription" {
  count = local.slack_notifications_enabled ? 1 : 0

  topic_arn = module.sns.topic_arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.sns_to_slack[0].arn
}

resource "aws_lambda_permission" "sns_to_slack_permission" {
  count = local.slack_notifications_enabled ? 1 : 0

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
  # arm64: 20% cheaper, and the script is pure boto3. python3.8 was a deprecated
  # runtime (AWS refuses new function creation on it), so fresh stacks would
  # have failed here anyway.
  runtime       = "python3.12"
  architectures = ["arm64"]
  role          = aws_iam_role.lambda_execution[0].arn
  # This was never set, so the function ran on AWS's 3s default while doing a
  # FilterLogEvents plus a StartReplication on a cold arm64 start. Real invocations
  # land at 2.7-2.9s and one has already been killed outright ("Status: timeout",
  # 3000.00 ms), recovered only because Lambda retried the async invocation. That
  # rescue is not guaranteed: once the async retries are spent, a restart lost this
  # way is silent, and the replication sits failed against the 48h deprovision clock
  # with nothing but the CRITICAL state-change alert to say so. 30s matches
  # sns_to_slack above; Lambda bills duration actually used, so a higher ceiling
  # costs nothing on the normal 2s path.
  timeout = 30

  source_code_hash = data.archive_file.dms_restart_lambda[0].output_base64sha256

  # Restart allowlist: the BI task only. The state-change rule also forwards
  # aurora migration task events (they must PAGE - a soak-phase STOP_TASK stop
  # sitting unnoticed past binlog retention kills the migration), but the
  # lambda's reload-target restart is destructive to a staged migration: it
  # re-runs a full load into a target the runner has already loaded, with the
  # FK-off Initstmt long removed (live-fired on m3-emea 2026-07-29 after a
  # validation-sweep OOM). The migration runner owns that task's lifecycle.
  environment {
    variables = {
      RESTARTABLE_TASK_ARNS = join(",", [for r in aws_dms_replication_config.this : r.arn])
    }
  }

  tags = local.tags
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
#
# KNOWN NON-FUNCTIONAL: ReplicationLatency (like BytesPendingReplication and
# OperationsPendingReplication, but unlike OperationsFailedReplication) is
# published in the DESTINATION bucket's region, and this alarm lives in the
# primary region, so it never sees a datapoint and notBreaching keeps it
# silently OK. Moving it needs a DR-region notification path (the dr_paging
# topic only exists when Aurora DR is on), so it stays here, documented,
# until the central-metrics parity work covers cross-region signals.
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

  tags = local.tags
}

# Dormant from 2026-06 until the replication rules gained their metrics block
# (dr.tf): the metric did not exist before that, so this alarm had nothing to
# watch. OperationsFailedReplication is the one replication metric published
# in the SOURCE region, so unlike the latency alarm above this one is in the
# right place, and it also counts Batch Replication task failures (though not
# a job that never runs at all - the rollout runbook's describe-job check
# covers that).
resource "aws_cloudwatch_metric_alarm" "dr_s3_replication_failed" {
  for_each = var.enable_dr ? aws_s3_bucket.guide_buckets : {}

  alarm_name          = "${local.identifier}-dr-s3-replication-failed-${each.key}"
  alarm_description   = "S3 DR replication to ${aws_s3_bucket.dr_guide_buckets[each.key].id} is failing for ${local.identifier} ${each.key}. Failed operations are NOT retried indefinitely. Diagnose (role trust/policy, KMS, destination), read the batch-replication-report CSVs in the logging bucket, then re-drive the gap with a Batch Replication job (util/create-s3-batch.sh)."
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

  tags = local.tags
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

# --- NLB target-group healthy-host alarms --- #
#
# module.nlb (nlb.tf) creates the app NLB and its two target groups (app: 443, acme: 80)
# directly in this stack - they are NOT looked up dynamically. Only target *registration*
# is dynamic: the logical layer's TargetGroupBindings (shipped in the dozuki chart) bind
# Envoy pod IPs into these target groups at runtime. So no data-source discovery is needed
# here - for_each just walks module.nlb.target_groups, the same map outputs.tf already
# reads for nlb_https_target_group_arn / nlb_http_target_group_arn. Because the alarms are
# created in the same apply as the NLB, a fresh install has no ordering hazard: there is no
# window where the alarms exist but the target groups do not (or vice versa).
#
# treat_missing_data = "missing" on both: AWS/NetworkELB HealthyHostCount is only reported
# while the target group has registered targets (see the "Reporting criteria" column for
# HealthyHostCount at
# https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-cloudwatch-metrics.html).
# module.nlb creates the target groups in this (physical) apply, but pod-IP registration
# only happens later when the logical layer installs the chart's TargetGroupBinding - so on
# every fresh install this metric has no datapoints at all for the whole physical -> logical
# -> chart-install window. "breaching" would page for that entire window on every new
# install; "missing" still catches the incident this alarm exists for (targets registered
# and publishing 0) identically, since that case reports real 0-valued datapoints, not an
# absence.
locals {
  nlb_alarm_target_groups = var.nlb_alarms_enabled ? module.nlb.target_groups : {}

  # No per-env envoy replica count variable exists in this stack (physical or logical) to
  # derive a warning threshold from. 2 mirrors the chart's steady-state envoy-gateway proxy
  # replica count. If a real variable for that count is ever added here, wire the warning
  # alarm's threshold to it instead of this constant.
  nlb_alarm_desired_healthy_hosts = 2
}

resource "aws_cloudwatch_metric_alarm" "nlb_healthy_hosts_critical" {
  for_each = local.nlb_alarm_target_groups

  alarm_name          = "${local.identifier}-nlb-${each.key}-healthy-hosts-critical"
  alarm_description   = "CRITICAL: ${local.identifier} NLB target group '${each.key}' (port ${each.value.port}) has zero healthy hosts - traffic on this listener is failing"
  comparison_operator = "LessThanOrEqualToThreshold"
  threshold           = 0
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  period              = 60
  namespace           = "AWS/NetworkELB"
  metric_name         = "HealthyHostCount"
  # Maximum, not Minimum: the metric is per-AZ and an AZ can publish 0 during a
  # partial degradation while another AZ still serves (measured live: min=0
  # max=1 with one replica up). Maximum reads 0 only when no AZ sees a healthy
  # target, which is the actual full-outage condition this alarm means. Partial
  # degradation is the warning alarm's job.
  statistic          = "Maximum"
  treat_missing_data = "missing"

  dimensions = {
    LoadBalancer = module.nlb.arn_suffix
    TargetGroup  = each.value.arn_suffix
  }

  alarm_actions = [module.sns.topic_arn]
  ok_actions    = [module.sns.topic_arn]
  tags          = local.tags
}

resource "aws_cloudwatch_metric_alarm" "nlb_healthy_hosts_warning" {
  for_each = local.nlb_alarm_target_groups

  alarm_name          = "${local.identifier}-nlb-${each.key}-healthy-hosts-warning"
  alarm_description   = "WARNING: ${local.identifier} NLB target group '${each.key}' (port ${each.value.port}) has fewer than ${local.nlb_alarm_desired_healthy_hosts} healthy hosts for 5 straight minutes - degraded capacity, not yet a full outage"
  comparison_operator = "LessThanThreshold"
  threshold           = local.nlb_alarm_desired_healthy_hosts
  evaluation_periods  = 5
  datapoints_to_alarm = 5
  period              = 60
  namespace           = "AWS/NetworkELB"
  metric_name         = "HealthyHostCount"
  statistic           = "Minimum"
  treat_missing_data  = "missing"

  dimensions = {
    LoadBalancer = module.nlb.arn_suffix
    TargetGroup  = each.value.arn_suffix
  }

  alarm_actions = [module.sns.topic_arn]
  ok_actions    = [module.sns.topic_arn]
  tags          = local.tags
}
