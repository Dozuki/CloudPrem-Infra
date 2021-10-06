module "sns" {
  source  = "terraform-aws-modules/sns/aws"
  version = "2.1.0"
  name    = local.identifier
}

module "cpu_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "2.1.0"

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
    AutoScalingGroupName = lookup(lookup(lookup(module.eks_cluster.node_groups["workers"], "resources")[0], "autoscaling_groups")[0], "name")
    //    AutoScalingGroupName = module.eks_cluster.workers_asg_names[0]
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
  version = "2.1.0"

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
  version = "2.1.0"

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
    AutoScalingGroupName = lookup(lookup(lookup(module.eks_cluster.node_groups["workers"], "resources")[0], "autoscaling_groups")[0], "name")
    //    AutoScalingGroupName = module.eks_cluster.workers_asg_names[0]
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
  version = "2.1.0"

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
    AutoScalingGroupName = lookup(lookup(lookup(module.eks_cluster.node_groups["workers"], "resources")[0], "autoscaling_groups")[0], "name")
    //    AutoScalingGroupName = module.eks_cluster.workers_asg_names[0]
  }

  alarm_actions = [
    module.sns.this_sns_topic_arn
  ]

  ok_actions = [
    module.sns.this_sns_topic_arn
  ]
}