resource "helm_release" "container_insights" {
  name  = "container-insights"
  chart = "${path.module}/charts/container_insights"

  namespace = "default"

  set {
    name  = "cluster_name"
    value = var.cluster_name
  }

  set {
    name  = "region_name"
    value = var.region_name
  }
}