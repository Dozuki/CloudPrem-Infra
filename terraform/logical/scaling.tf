
resource "helm_release" "cluster_autoscaler" {
  name      = "cluster-autoscaler"
  chart     = "charts/cluster-autoscaler"
  namespace = "kube-system"

  values = [
    templatefile("static/cluster-autoscaler-values.yaml", {
      account_id   = data.aws_caller_identity.current.account_id,
      partition    = data.aws_partition.current.partition,
      role_name    = var.eks_oidc_cluster_access_role_name,
      cluster_name = var.eks_cluster_id
    })
  ]

  set {
    name  = "awsRegion"
    value = data.aws_region.current.name
  }
  set {
    name  = "autoDiscovery.clusterName"
    value = var.eks_cluster_id
  }
}