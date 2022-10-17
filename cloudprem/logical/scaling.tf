
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

resource "helm_release" "metrics_server" {
  name  = "metrics-server"
  chart = "charts/metrics-server"
}
data "kubernetes_all_namespaces" "allns" {
  depends_on = [helm_release.replicated]
}

resource "kubernetes_horizontal_pod_autoscaler" "app" {
  depends_on = [helm_release.replicated]
  metadata {
    name = "app-hpa"
    # Terraform magic required to find the randomly created replicated namespace so we can install the HPAs in the right place.
    namespace = coalesce([for i, v in data.kubernetes_all_namespaces.allns.namespaces : try(regexall("replicated\\-.*", v)[0], "")]...)
  }

  spec {
    min_replicas = 2
    max_replicas = 30

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
    name = "queueworkerd-hpa"
    # Terraform magic required to find the randomly created replicated namespace so we can install the HPAs in the right place.
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
