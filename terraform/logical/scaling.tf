
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

resource "kubernetes_horizontal_pod_autoscaler" "app" {
  depends_on = [local_file.replicated_install]

  metadata {
    name      = "app-hpa"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
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
  depends_on = [local_file.replicated_install]

  metadata {
    name      = "queueworkerd-hpa"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
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
