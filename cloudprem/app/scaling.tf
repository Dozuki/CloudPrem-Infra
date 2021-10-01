data "kubernetes_all_namespaces" "allns" {
  depends_on = [helm_release.replicated]
}

resource "kubernetes_horizontal_pod_autoscaler" "app" {
  depends_on = [helm_release.replicated]
  metadata {
    name      = "app-hpa"
    namespace = coalesce([for i, v in data.kubernetes_all_namespaces.allns.namespaces : try(regexall("replicated\\-.*", v)[0], "")]...)
  }

  spec {
    min_replicas = 2
    max_replicas = 50

    scale_target_ref {
      kind        = "Deployment"
      name        = "app-deployment"
      api_version = "apps/v1"
    }

    metric {
      type = "Resource"
      resource {
        name = "memory"
        target {
          type                = "Utilization"
          average_utilization = "80"
        }
      }
    }

    metric {
      type = "Resource"
      resource {
        name = "cpu"
        target {
          type                = "Utilization"
          average_utilization = "50"
        }
      }
    }
  }
}

resource "kubernetes_horizontal_pod_autoscaler" "queueworkerd" {
  depends_on = [helm_release.replicated]
  metadata {
    name      = "queueworkerd-hpa"
    namespace = coalesce([for i, v in data.kubernetes_all_namespaces.allns.namespaces : try(regexall("replicated\\-.*", v)[0], "")]...)
  }

  spec {
    min_replicas = 2
    max_replicas = 50

    scale_target_ref {
      kind        = "Deployment"
      name        = "queueworkerd-deployment"
      api_version = "apps/v1"
    }

    metric {
      type = "Resource"
      resource {
        name = "memory"
        target {
          type                = "Utilization"
          average_utilization = "80"
        }
      }
    }

    metric {
      type = "Resource"
      resource {
        name = "cpu"
        target {
          type                = "Utilization"
          average_utilization = "80"
        }
      }
    }
  }
}