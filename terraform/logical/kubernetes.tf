resource "kubernetes_storage_class_v1" "ebs_gp3" {
  count = var.cloud == "aws" ? 1 : 0

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

resource "kubernetes_namespace_v1" "app" {
  metadata {
    name = local.k8s_namespace_name
  }
}

resource "kubernetes_role_v1" "dozuki_subsite_role" {
  metadata {
    name      = "dozuki_subsite_role"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }

  rule {
    api_groups = ["infra.dozuki.com"]
    resources  = ["subsites"]
    verbs      = ["get", "list", "watch", "create", "delete"]
  }
}


resource "kubernetes_role_binding_v1" "dozuki_subsite_role_binding" {

  metadata {
    name      = "dozuki_subsite_role_binding"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role_v1.dozuki_subsite_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
}

resource "kubernetes_cluster_role_v1" "dozuki_list_role" {

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

resource "kubernetes_cluster_role_binding_v1" "dozuki_list_role_binding" {

  metadata {
    name = "dozuki_list_role_binding"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role_v1.dozuki_list_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
}

resource "kubernetes_namespace_v1" "cert_manager" {
  metadata {
    name = "cert-manager"
  }
}

resource "helm_release" "cert_manager" {
  name  = "cert-manager"
  chart = "${path.module}/charts/cert-manager"

  namespace = kubernetes_namespace_v1.cert_manager.metadata[0].name

  wait = true

  set {
    name  = "crds.enabled"
    value = "true"
  }
  set {
    name  = "crds.keep"
    value = "true"
  }
  set {
    name  = "config.enableGatewayAPI"
    value = "true"
  }
}

resource "helm_release" "envoy_gateway" {
  name       = "envoy-gateway"
  namespace  = "envoy-gateway-system"
  repository = "oci://docker.io/envoyproxy"
  chart      = "gateway-helm"
  version    = "v1.7.0"

  create_namespace = true
  wait             = true
}

# Stable Service in envoy-gateway-system targeting Envoy proxy pods.
# Proxy pods are deployed in the controller namespace, not the Gateway namespace.
resource "kubernetes_service_v1" "envoy_proxy" {
  count      = var.cloud == "aws" ? 1 : 0
  depends_on = [helm_release.app]

  metadata {
    name      = "dozuki-envoy-proxy"
    namespace = "envoy-gateway-system"
  }
  spec {
    type = "ClusterIP"
    selector = {
      "gateway.envoyproxy.io/owning-gateway-name"      = "dozuki-gateway"
      "gateway.envoyproxy.io/owning-gateway-namespace" = kubernetes_namespace_v1.app.metadata[0].name
    }
    port {
      name        = "https"
      port        = 443
      target_port = 10443
      protocol    = "TCP"
    }
    port {
      name        = "http"
      port        = 80
      target_port = 10080
      protocol    = "TCP"
    }
  }
}

# Azure twin of the Envoy proxy Service: exposes the proxy pods directly via an
# Azure Load Balancer instead of NLB target group bindings.
resource "kubernetes_service_v1" "envoy_proxy_azure" {
  count      = var.cloud == "azure" ? 1 : 0
  depends_on = [helm_release.app]

  # Azure LB uses its default TCP health probe. Do not set an HTTP
  # health-probe-request-path annotation unless the Envoy proxy is verified to
  # serve 200 on that path on the data ports (10443/10080) — a failing HTTP
  # probe blackholes ingress.
  metadata {
    name      = "dozuki-envoy-proxy"
    namespace = "envoy-gateway-system"
  }
  spec {
    type = "LoadBalancer"
    selector = {
      "gateway.envoyproxy.io/owning-gateway-name"      = "dozuki-gateway"
      "gateway.envoyproxy.io/owning-gateway-namespace" = kubernetes_namespace_v1.app.metadata[0].name
    }
    port {
      name        = "https"
      port        = 443
      target_port = 10443
      protocol    = "TCP"
    }
    port {
      name        = "http"
      port        = 80
      target_port = 10080
      protocol    = "TCP"
    }
  }
}

resource "kubernetes_manifest" "tgb_https" {
  count      = var.cloud == "aws" ? 1 : 0
  depends_on = [kubernetes_service_v1.envoy_proxy]

  manifest = {
    apiVersion = "eks.amazonaws.com/v1"
    kind       = "TargetGroupBinding"
    metadata = {
      name      = "envoy-https"
      namespace = "envoy-gateway-system"
    }
    spec = {
      serviceRef = {
        name = "dozuki-envoy-proxy"
        port = 443
      }
      targetGroupARN = var.nlb_https_target_group_arn
      targetType     = "ip"
    }
  }
}

resource "kubernetes_manifest" "tgb_http" {
  count      = var.cloud == "aws" ? 1 : 0
  depends_on = [kubernetes_service_v1.envoy_proxy]

  manifest = {
    apiVersion = "eks.amazonaws.com/v1"
    kind       = "TargetGroupBinding"
    metadata = {
      name      = "envoy-http"
      namespace = "envoy-gateway-system"
    }
    spec = {
      serviceRef = {
        name = "dozuki-envoy-proxy"
        port = 80
      }
      targetGroupARN = var.nlb_http_target_group_arn
      targetType     = "ip"
    }
  }
}

resource "kubernetes_manifest" "nodepool_spot" {
  count = var.cloud == "aws" ? 1 : 0

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
  count = var.cloud == "aws" ? 1 : 0

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
  depends_on = [helm_release.cert_manager]

  name       = "external-secrets"
  namespace  = kubernetes_namespace_v1.app.metadata[0].name
  repository = "https://charts.external-secrets.io"
  chart      = "external-secrets"

  wait = true

  set {
    name  = "crds.createClusterExternalSecret"
    value = "true"
  }
  set {
    name  = "crds.createClusterSecretStore"
    value = "true"
  }
}

# Service account for ESO to authenticate to Vault via K8s auth.
# The SecretStore template references this SA by name.
resource "kubernetes_service_account_v1" "eso_vault_auth" {
  metadata {
    name      = "dozuki-external-secrets"
    namespace = kubernetes_namespace_v1.app.metadata[0].name

    annotations = var.cloud == "azure" ? {
      "azure.workload.identity/client-id" = var.azure_eso_identity_client_id
    } : {}

    labels = var.cloud == "azure" ? {
      "azure.workload.identity/use" = "true"
    } : {}
  }
}

# CloudWatch Observability add-on — installed in logical (not physical) because
# Auto Mode won't provision nodes until workloads are scheduled. cert-manager
# and envoy-gateway trigger node creation; this addon installs after nodes exist.
resource "aws_eks_addon" "cloudwatch_observability" {
  count        = var.cloud == "aws" ? 1 : 0
  cluster_name = data.aws_eks_cluster.main[0].name
  addon_name   = "amazon-cloudwatch-observability"
  depends_on   = [helm_release.cert_manager]
}

resource "helm_release" "app" {
  depends_on = [helm_release.cert_manager, helm_release.envoy_gateway, helm_release.external_secrets, kubernetes_service_account_v1.eso_vault_auth, aws_eks_addon.cloudwatch_observability, helm_release.seaweedfs, kubernetes_job_v1.seaweedfs_buckets]

  name      = "dozuki"
  namespace = kubernetes_namespace_v1.app.metadata[0].name

  chart = "${path.module}/charts/dozuki/chart"

  # wait must be true so that on destroy, Helm waits for all resources
  # (including custom resources with finalizers like Gateway, HTTPRoute,
  # Certificate, ClusterIssuer) to be fully deleted before Terraform
  # proceeds to destroy the controllers (cert-manager, envoy-gateway)
  # that process those finalizers. Without this, controllers are torn
  # down while custom resources still have pending finalizers, causing
  # the namespace to hang indefinitely.
  wait              = true
  timeout           = 900
  dependency_update = true

  # --- General ---
  set {
    name  = "hostname"
    value = var.dns_domain_name
  }
  set {
    name  = "dns_validation"
    value = var.cloud == "aws" && !local.is_us_gov && contains(["dozuki.cloud", "dozuki.com", "dozuki.app", "dozuki.guide"], replace(var.dns_domain_name, "/^[^.]+\\./", "")) ? "true" : "false"
  }
  set {
    name  = "customer"
    value = coalesce(var.customer, "Dozuki")
  }
  set {
    name  = "environment"
    value = var.environment
  }

  # --- AWS ---
  set {
    name  = "aws.region"
    value = var.cloud == "aws" ? data.aws_region.current[0].id : ""
  }
  set {
    name  = "aws.accountId"
    value = var.cloud == "aws" ? data.aws_caller_identity.current[0].account_id : ""
  }
  set {
    name  = "aws.enabled"
    value = var.cloud == "aws" ? "true" : "false"
  }

  # --- Database ---
  set {
    name  = "db.host"
    value = local.db_master_host
  }
  set {
    name  = "db.user"
    value = local.db_master_username
  }
  set {
    name  = "db.rdsCaCert"
    value = var.cloud == "aws" ? base64encode(file(local.ca_cert_pem_file)) : ""
  }
  set_sensitive {
    name  = "db.password"
    value = local.db_master_password
  }

  # --- SMTP ---
  set {
    name  = "smtp.enabled"
    value = var.smtp_enabled ? "true" : "false"
  }
  set {
    name  = "smtp.host"
    value = var.smtp_host
  }
  set {
    name  = "smtp.from"
    value = var.smtp_from_address
  }
  set {
    name  = "smtp.auth.enabled"
    value = var.smtp_auth_enabled ? "true" : "false"
  }
  set {
    name  = "smtp.auth.username"
    value = var.smtp_username
  }
  set_sensitive {
    name  = "smtp.auth.password"
    value = var.smtp_password
  }

  # --- Sentry ---
  set {
    name  = "sentry.customerName"
    value = coalesce(var.customer, "Dozuki")
  }

  # --- Images ---
  set {
    name  = "images.app.repository"
    value = var.image_repository
  }
  set {
    name  = "images.app.tag"
    value = var.image_tag
  }
  set {
    name  = "images.webnextjs.tag"
    value = var.nextjs_tag
  }

  # --- Ingress / Gateway ---
  set {
    name  = "ingress.hosts[0].hostname"
    value = coalesce(var.ingress_hostname, var.dns_domain_name)
  }
  set {
    name  = "gateway.hosts[0].hostname"
    value = coalesce(var.ingress_hostname, var.dns_domain_name)
  }
  set {
    name  = "gateway.hosts[0].tlsSecretName"
    value = "tls-secret"
  }

  # --- Webhooks ---
  set {
    name  = "webhooks.enabled"
    value = var.enable_webhooks ? "true" : "false"
  }

  # --- Object Storage ---
  set {
    name  = "objectStorage.kmsKey"
    value = var.cloud == "aws" ? data.aws_kms_key.s3[0].arn : ""
  }
  set {
    name  = "objectStorage.imagesBucket"
    value = var.s3_images_bucket
  }
  set {
    name  = "objectStorage.pdfsBucket"
    value = var.s3_pdfs_bucket
  }
  set {
    name  = "objectStorage.documentsBucket"
    value = var.s3_documents_bucket
  }
  set {
    name  = "objectStorage.objectsBucket"
    value = var.s3_objects_bucket
  }

  # --- Memcached ---
  set {
    name  = "memcached.host"
    value = var.cloud == "aws" ? var.memcached_cluster_address : "dozuki-memcached"
  }

  # --- Vault ---
  set {
    name  = "vault.enabled"
    value = var.cloud == "aws" ? "true" : "false"
  }
  set {
    name  = "vault.address"
    value = var.vault_address
  }

  # --- Azure ---
  set {
    name  = "azure.enabled"
    value = var.cloud == "azure" ? "true" : "false"
  }
  set {
    name  = "azure.tenantId"
    value = var.azure_tenant_id
  }
  set {
    name  = "azure.keyVaultUri"
    value = var.azure_key_vault_uri
  }
  set {
    name  = "azure.environment"
    value = var.azure_environment
  }

  # --- Monitoring ---
  set {
    name  = "monitoring.enabled"
    value = "true"
  }

  # --- In-cluster services (Azure) ---
  set {
    name  = "memcached.enabled"
    value = var.cloud == "azure" ? "true" : "false"
  }
  set {
    name  = "objectStorage.endpoint"
    value = var.cloud == "azure" ? local.seaweedfs_s3_endpoint : ""
  }
  set_sensitive {
    name  = "objectStorage.credentials.accessKey"
    value = var.cloud == "azure" ? try(random_password.seaweedfs_access_key[0].result, "") : ""
  }
  set_sensitive {
    name  = "objectStorage.credentials.secretKey"
    value = var.cloud == "azure" ? try(random_password.seaweedfs_secret_key[0].result, "") : ""
  }

  # --- Google Translate ---
  set_sensitive {
    name  = "googleTranslate.token"
    value = var.google_translate_api_token
  }

  # --- Grafana ---
  set {
    name  = "grafana.enabled"
    value = var.enable_bi ? "true" : "false"
  }
  set {
    name  = "grafana.security.admin_user"
    value = local.grafana_admin_username
  }
  set {
    name  = "grafana.datasource.host"
    value = local.db_bi_host
  }
  set_sensitive {
    name  = "grafana.security.admin_password"
    value = local.grafana_admin_password
  }
  set_sensitive {
    name  = "grafana.datasource.password"
    value = local.db_bi_password
  }

  # --- Connectivity (sunset planned) ---
  set {
    name  = "connectivity.webhook-service.messageBroker.brokerList"
    value = var.msk_bootstrap_brokers
  }
  set {
    name  = "connectivity.webhook-service.mysql.host"
    value = local.db_master_host
  }
  set {
    name  = "connectivity.webhook-service.mysql.username"
    value = local.db_master_username
  }
  set {
    name  = "connectivity.webhook-service.mongo.connectionString"
    value = "mongodb://dozuki-mongodb/webhooks"
  }
  set {
    name  = "connectivity.integrations-service.messageBroker.brokerList"
    value = var.msk_bootstrap_brokers
  }
  set {
    name  = "connectivity.integrations-service.mongo.connectionString"
    value = "mongodb://dozuki-mongodb/integrations"
  }
  set {
    name  = "connectivity.event-service.database.host"
    value = local.db_master_host
  }
  set {
    name  = "connectivity.event-service.database.username"
    value = local.db_master_username
  }
  set {
    name  = "connectivity.event-service.messageBroker.brokerList"
    value = var.msk_bootstrap_brokers
  }
  set {
    name  = "connectivity.event-service.redis.host"
    value = "dozuki-redis-master"
  }
  set {
    name  = "connectivity.event-service.redis.tls"
    value = "false"
  }
  set {
    name  = "connectivity.connectors-worker.messageBroker.brokerList"
    value = var.msk_bootstrap_brokers
  }
  set {
    name  = "connectivity.connectors-worker.redis.host"
    value = "dozuki-redis-master"
  }
  set {
    name  = "connectivity.connectors-worker.redis.tls"
    value = "false"
  }

  lifecycle {
    precondition {
      condition     = var.cloud == "aws" || (!var.enable_webhooks && !var.enable_bi)
      error_message = "enable_webhooks and enable_bi are not supported on Azure."
    }
  }
}
# Moved blocks: these resources gained `count` when Azure support was added.
# They keep existing AWS state addresses from churning.
moved {
  from = kubernetes_storage_class_v1.ebs_gp3
  to   = kubernetes_storage_class_v1.ebs_gp3[0]
}

moved {
  from = kubernetes_service_v1.envoy_proxy
  to   = kubernetes_service_v1.envoy_proxy[0]
}

moved {
  from = kubernetes_manifest.tgb_https
  to   = kubernetes_manifest.tgb_https[0]
}

moved {
  from = kubernetes_manifest.tgb_http
  to   = kubernetes_manifest.tgb_http[0]
}

moved {
  from = kubernetes_manifest.nodepool_spot
  to   = kubernetes_manifest.nodepool_spot[0]
}

moved {
  from = kubernetes_manifest.nodepool_on_demand
  to   = kubernetes_manifest.nodepool_on_demand[0]
}

moved {
  from = aws_eks_addon.cloudwatch_observability
  to   = aws_eks_addon.cloudwatch_observability[0]
}
