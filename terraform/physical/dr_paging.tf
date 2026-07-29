# DR detection + paging (failover-automation design P2).
#
# The in-region monitoring stack cannot be the sole detector of its own region's death,
# so the dead-man chain lives ENTIRELY in the DR region: the headless secondary emits
# AuroraGlobalDBReplicationLag into DR-region CloudWatch, and when the primary dies (or
# replication silently breaks) that metric stalls or stops - treat_missing_data =
# breaching turns "the primary went quiet" into the page. No primary-region API, SNS or
# Lambda anywhere in the path.
#
# The page prompts a HUMAN with the exact cpi-dr commands; it never triggers a
# promotion. Automating the execution but not the decision is the design's one
# non-negotiable (split-brain has no merge path).

locals {
  operations_owner = var.operations_owner != "" ? var.operations_owner : (var.managed_private_cloud ? "dozuki" : "customer")

  # The page. Everything the on-call needs to act safely, in the alarm body itself -
  # the incident may have taken the wiki/dashboards with it. Kept under CloudWatch's
  # 1024-char description limit.
  dr_page = local.aurora_dr_enabled ? join("\n", [
    "DR DEAD-MAN: ${local.identifier} (owner: ${local.operations_owner}). Aurora Global DB replication into ${var.dr_region} has stalled or stopped reporting for 15m - the primary region is down, or replication is broken.",
    "INDEPENDENTLY VERIFY FIRST. Promoting while the primary is alive causes split-brain with no merge path; a reachable primary takes a planned managed failover instead. cpi-dr enforces this and has no force flag.",
    "1) diagnose (read-only): cpi-dr status --dr-region ${var.dr_region} --global-cluster ${try(aws_rds_global_cluster.aurora[0].id, local.identifier)}",
    "2) if failover MIGHT be needed (reversible): cpi-dr prepare --dr-region ${var.dr_region} --global-cluster ${try(aws_rds_global_cluster.aurora[0].id, local.identifier)}",
    "3) only when the primary is CONFIRMED gone: cpi-dr promote (then cpi-dr rebuild).",
    "Runbook: aurora-global-db-failover.",
  ]) : ""
}

resource "aws_sns_topic" "dr_paging" {
  count    = local.aurora_dr_enabled ? 1 : 0
  provider = aws.dr

  name = "${local.identifier}-dr-paging"
  tags = local.tags
}

# The acknowledged path: PagerDuty/Opsgenie SNS inbound URL (auto-confirms).
resource "aws_sns_topic_subscription" "dr_paging_endpoint" {
  count    = local.aurora_dr_enabled && var.dr_paging_endpoint != "" ? 1 : 0
  provider = aws.dr

  topic_arn              = aws_sns_topic.dr_paging[0].arn
  protocol               = "https"
  endpoint               = var.dr_paging_endpoint
  endpoint_auto_confirms = true
}

# Email fallback so the page at least lands SOMEWHERE while no paging integration is
# configured. Email is notification, not paging - the check below keeps that visible.
resource "aws_sns_topic_subscription" "dr_paging_email" {
  count    = local.aurora_dr_enabled && var.dr_paging_endpoint == "" && var.alarm_email != "" ? 1 : 0
  provider = aws.dr

  topic_arn = aws_sns_topic.dr_paging[0].arn
  protocol  = "email"
  endpoint  = var.alarm_email
}

# The dead-man. Maximum lag over 60s periods; 15/15 breaching datapoints pages. The
# 15-minute window rides out fresh-stack metric warmup and transient lag spikes while
# still paging well inside the ~50min rebuild RTO; missing data breaches because a dead
# primary emits nothing.
resource "aws_cloudwatch_metric_alarm" "dr_replication_deadman" {
  count    = local.aurora_dr_enabled ? 1 : 0
  provider = aws.dr

  alarm_name        = "${local.identifier}-dr-replication-deadman"
  alarm_description = local.dr_page

  namespace           = "AWS/RDS"
  metric_name         = "AuroraGlobalDBReplicationLag"
  statistic           = "Maximum"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 300000 # ms - 5 minutes of replication lag is itself page-worthy
  period              = 60
  evaluation_periods  = 15
  datapoints_to_alarm = 15
  treat_missing_data  = "breaching"

  dimensions = {
    DBClusterIdentifier = aws_rds_cluster.dr_aurora_secondary[0].cluster_identifier
  }

  alarm_actions = [aws_sns_topic.dr_paging[0].arn]
  ok_actions    = [aws_sns_topic.dr_paging[0].arn]

  tags = local.tags
}

# A stack with DR but no acknowledged paging integration has DR detection that emails
# into the void. Say so on every plan rather than pretending email suffices.
check "dr_paging_operational" {
  assert {
    condition     = !local.aurora_dr_enabled || var.dr_paging_endpoint != ""
    error_message = "DR is enabled but dr_paging_endpoint is unset: the DR dead-man only emails alarm_email, so nobody is paged and nothing escalates. DR detection is NOT operationally enabled until an acknowledged paging endpoint (PagerDuty/Opsgenie SNS URL) is configured."
  }
}
