
resource "helm_release" "cluster_autoscaler" {
  depends_on = [local_file.cluster_autoscaler_helmignore]

  name      = "cluster-autoscaler"
  chart     = "charts/cluster-autoscaler"
  namespace = "kube-system"

  values = [
    templatefile("static/cluster-autoscaler-values.yaml", {
      account_id = data.aws_caller_identity.current.account_id,
      partition  = data.aws_partition.current.partition,
      role_name  = var.eks_oidc_cluster_access_role_name
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
  depends_on = [local_file.metrics_server_helmignore]

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
    max_replicas = 10

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

resource "local_file" "cluster_autoscaler_helmignore" {
  content         = file("${path.module}/charts/helmignore")
  filename        = "${path.module}/charts/cluster-autoscaler/.helmignore"
  file_permission = "0644"
}
resource "local_file" "metrics_server_helmignore" {
  content         = file("${path.module}/charts/helmignore")
  filename        = "${path.module}/charts/metrics-server/.helmignore"
  file_permission = "0644"
}