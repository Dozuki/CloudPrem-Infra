resource "kubernetes_storage_class_v1" "ebs_gp3" {
  metadata {
    name = "ebs-gp3"
    annotations = {
      "storageclass.kubernetes.io/is-default-class" = "true"
    }
  }
  storage_provisioner    = "ebs.csi.eks.amazonaws.com"
  reclaim_policy         = "Delete"
  volume_binding_mode    = "WaitForFirstConsumer"
  allow_volume_expansion = true
  parameters = {
    type      = "gp3"
    encrypted = "true"
  }
}

resource "kubernetes_namespace" "app" {
  metadata {
    name = local.k8s_namespace_name
  }
}

resource "kubernetes_role" "dozuki_subsite_role" {
  metadata {
    name      = "dozuki_subsite_role"
    namespace = kubernetes_namespace.app.metadata[0].name
  }

  rule {
    api_groups = ["infra.dozuki.com"]
    resources  = ["subsites"]
    verbs      = ["get", "list", "watch", "create", "delete"]
  }
}


resource "kubernetes_role_binding" "dozuki_subsite_role_binding" {

  metadata {
    name      = "dozuki_subsite_role_binding"
    namespace = kubernetes_namespace.app.metadata[0].name
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role.dozuki_subsite_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = kubernetes_namespace.app.metadata[0].name
  }
}

resource "kubernetes_cluster_role" "dozuki_list_role" {

  metadata {
    name = "dozuki_list_role"
  }

  rule {
    api_groups = ["apps"]
    resources  = ["deployments", "daemonsets"]
    verbs      = ["get", "list", "watch"]
  }
  rule {
    api_groups = [""]
    resources  = ["pods"]
    verbs      = ["list"]
  }
}

resource "kubernetes_cluster_role_binding" "dozuki_list_role_binding" {

  metadata {
    name = "dozuki_list_role_binding"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.dozuki_list_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = kubernetes_namespace.app.metadata[0].name
  }
}

resource "kubernetes_secret" "dozuki_infra_credentials" {
  count = var.enable_vault ? 0 : 1

  metadata {
    name      = "dozuki-infra-credentials"
    namespace = kubernetes_namespace.app.metadata[0].name
  }
  type = "Opaque"

  data = {
    master_host     = local.db_master_host
    master_user     = local.db_master_username
    master_password = local.db_master_password
    bi_host         = local.db_bi_host
    bi_user         = local.db_master_username
    bi_password     = local.db_bi_password
    memcached_host  = var.memcached_cluster_address
  }
}

resource "kubernetes_namespace" "cert_manager" {
  metadata {
    name = "cert-manager"
  }
}

resource "helm_release" "cert_manager" {
  name  = "cert-manager"
  chart = "${path.module}/charts/cert-manager"

  namespace = kubernetes_namespace.cert_manager.metadata[0].name

  wait = true

  set = [
    {
      name  = "crds.enabled"
      value = "true"
    },
    {
      name  = "crds.keep"
      value = "true"
    },
  ]
}

resource "kubernetes_manifest" "tgb_https" {
  depends_on = [helm_release.app]

  manifest = {
    apiVersion = "eks.amazonaws.com/v1"
    kind       = "TargetGroupBinding"
    metadata = {
      name      = "nginx-https"
      namespace = kubernetes_namespace.app.metadata[0].name
    }
    spec = {
      serviceRef = {
        name = "dozuki-ingress-nginx-controller"
        port = 443
      }
      targetGroupARN = var.nlb_https_target_group_arn
      targetType     = "ip"
    }
  }
}

resource "kubernetes_manifest" "tgb_http" {
  depends_on = [helm_release.app]

  manifest = {
    apiVersion = "eks.amazonaws.com/v1"
    kind       = "TargetGroupBinding"
    metadata = {
      name      = "nginx-http"
      namespace = kubernetes_namespace.app.metadata[0].name
    }
    spec = {
      serviceRef = {
        name = "dozuki-ingress-nginx-controller"
        port = 80
      }
      targetGroupARN = var.nlb_http_target_group_arn
      targetType     = "ip"
    }
  }
}

resource "kubernetes_manifest" "nodepool_spot" {
  manifest = {
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata = {
      name = "spot"
    }
    spec = {
      template = {
        spec = {
          nodeClassRef = {
            group = "eks.amazonaws.com"
            kind  = "NodeClass"
            name  = "default"
          }
          requirements = [
            {
              key      = "karpenter.sh/capacity-type"
              operator = "In"
              values   = ["spot"]
            },
            {
              key      = "kubernetes.io/arch"
              operator = "In"
              values   = ["amd64"]
            }
          ]
        }
      }
      disruption = {
        consolidationPolicy = "WhenEmptyOrUnderutilized"
        consolidateAfter    = "1m"
      }
      weight = 100
    }
  }
}

resource "kubernetes_manifest" "nodepool_on_demand" {
  manifest = {
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata = {
      name = "on-demand"
    }
    spec = {
      template = {
        spec = {
          nodeClassRef = {
            group = "eks.amazonaws.com"
            kind  = "NodeClass"
            name  = "default"
          }
          requirements = [
            {
              key      = "karpenter.sh/capacity-type"
              operator = "In"
              values   = ["on-demand"]
            },
            {
              key      = "kubernetes.io/arch"
              operator = "In"
              values   = ["amd64"]
            }
          ]
          taints = [
            {
              key    = "eks.amazonaws.com/capacity-type"
              value  = "on-demand"
              effect = "NoSchedule"
            }
          ]
        }
      }
      disruption = {
        consolidationPolicy = "WhenEmptyOrUnderutilized"
        consolidateAfter    = "1m"
      }
      weight = 10
    }
  }
}

resource "helm_release" "external_secrets" {
  count      = var.enable_vault ? 1 : 0
  depends_on = [helm_release.cert_manager]

  name       = "external-secrets"
  namespace  = kubernetes_namespace.app.metadata[0].name
  repository = "https://charts.external-secrets.io"
  chart      = "external-secrets"

  wait = true

  set = [
    {
      name  = "crds.createClusterExternalSecret"
      value = "true"
    },
    {
      name  = "crds.createClusterSecretStore"
      value = "true"
    },
  ]
}

# Service account for ESO to authenticate to Vault via K8s auth.
# The SecretStore template references this SA by name.
resource "kubernetes_service_account" "eso_vault_auth" {
  count = var.enable_vault ? 1 : 0

  metadata {
    name      = "dozuki-external-secrets"
    namespace = kubernetes_namespace.app.metadata[0].name
  }
}

resource "helm_release" "app" {
  depends_on = [helm_release.cert_manager, helm_release.external_secrets, kubernetes_service_account.eso_vault_auth]

  name      = "dozuki"
  namespace = kubernetes_namespace.app.metadata[0].name

  chart = "${path.module}/charts/dozuki/chart"

  wait              = false
  dependency_update = true

  values = [
    templatefile("static/app-values.yaml", local.all_config_values_flat)
  ]

}