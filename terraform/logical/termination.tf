

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