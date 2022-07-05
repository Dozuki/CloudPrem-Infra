

resource "helm_release" "aws_node_termination_handler" {
  name             = "aws-node-termination-handler"
  namespace        = "kube-system"
  chart            = "charts/aws-node-termination-handler"
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
    value = var.termination_handler_role_arn
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
    value = var.termination_handler_sqs_queue_id
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
  count                  = length(var.eks_worker_asg_names)
  name                   = "aws-node-termination-handler"
  autoscaling_group_name = var.eks_worker_asg_names[count.index]
  lifecycle_transition   = "autoscaling:EC2_INSTANCE_TERMINATING"
  heartbeat_timeout      = 300
  default_result         = "CONTINUE"
}
