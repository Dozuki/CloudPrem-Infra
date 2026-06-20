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

  set = [
    {
      name  = "crds.enabled"
      value = "true"
    },
    {
      name  = "crds.keep"
      value = "true"
    },
    {
      name  = "config.enableGatewayAPI"
      value = "true"
    },
  ]
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

# Image pull secret for GHCR (Azure only) — MPC images are pulled directly
# from ghcr.io instead of being mirrored into ACR.
resource "kubernetes_secret_v1" "ghcr_pull" {
  count = var.cloud == "azure" ? 1 : 0

  metadata {
    name      = "ghcr-pull"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }

  type = "kubernetes.io/dockerconfigjson"

  data = {
    ".dockerconfigjson" = jsonencode({
      auths = {
        "ghcr.io" = {
          auth = base64encode("${var.ghcr_pull_username}:${var.ghcr_pull_token}")
        }
      }
    })
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

locals {
  memcached_in_cluster = var.cloud == "azure" || var.memcached_in_cluster
}

resource "helm_release" "app" {
  depends_on = [helm_release.cert_manager, helm_release.envoy_gateway, helm_release.external_secrets, kubernetes_service_account_v1.eso_vault_auth, kubernetes_secret_v1.ghcr_pull, aws_eks_addon.cloudwatch_observability, helm_release.seaweedfs, kubernetes_job_v1.seaweedfs_buckets, kubernetes_secret_v1.gateway_tls, helm_release.external_dns]

  name      = "dozuki"
  namespace = kubernetes_namespace_v1.app.metadata[0].name

  chart      = "dozuki"
  repository = "oci://${var.image_repository}/charts"
  version    = var.chart_version

  # wait must be true so that on destroy, Helm waits for all resources
  # (including custom resources with finalizers like Gateway, HTTPRoute,
  # Certificate, ClusterIssuer) to be fully deleted before Terraform
  # proceeds to destroy the controllers (cert-manager, envoy-gateway)
  # that process those finalizers. Without this, controllers are torn
  # down while custom resources still have pending finalizers, causing
  # the namespace to hang indefinitely.
  wait    = true
  timeout = 900

  # Azure-only values: GHCR pull secret for MPC images, and expose the gateway
  # via an Azure public LoadBalancer (no NLB on Azure). An azure-dns-label-name
  # annotation gives the LB a stable <label>.<region>.cloudapp.azure.com FQDN.
  # On AWS this is an empty list of values files — a no-op, no overrides applied.
  values = var.cloud == "azure" ? [yamlencode(merge({
    global = { imagePullSecrets = [{ name = "ghcr-pull" }] }
    gateway = {
      service = {
        type        = "LoadBalancer"
        annotations = var.gateway_dns_label != "" ? { "service.beta.kubernetes.io/azure-dns-label-name" = var.gateway_dns_label } : {}
      }
      dnsTarget = local.lb_fqdn
    }
    }, var.azure_acme_server != "" ? {
    cert_manager = { acmeServer = var.azure_acme_server }
  } : {}))] : []

  # helm provider 3.x: set/set_sensitive are list-of-object attributes, not
  # repeatable blocks. Section groupings preserved as comments.
  set = concat([
    # --- General ---
    { name = "hostname", value = var.dns_domain_name },
    { name = "dns_validation", value = var.cloud == "aws" && !local.is_us_gov && !local.tls_manual && contains(["dozuki.cloud", "dozuki.com", "dozuki.app", "dozuki.guide"], replace(var.dns_domain_name, "/^[^.]+\\./", "")) ? "true" : "false" },
    { name = "customer", value = coalesce(var.customer, "Dozuki") },
    { name = "environment", value = var.environment },

    # --- AWS ---
    { name = "aws.region", value = var.cloud == "aws" ? data.aws_region.current[0].id : "us-east-1" },
    { name = "aws.accountId", value = var.cloud == "aws" ? data.aws_caller_identity.current[0].account_id : "" },
    { name = "aws.enabled", value = var.cloud == "aws" ? "true" : "false" },

    # --- Database ---
    { name = "db.host", value = local.db_master_host },
    { name = "db.user", value = local.db_master_username },
    { name = "db.rdsCaCert", value = base64encode(file(local.ca_cert_pem_file)) },

    # --- SMTP ---
    { name = "smtp.enabled", value = var.smtp_enabled ? "true" : "false" },
    { name = "smtp.host", value = var.smtp_host },
    { name = "smtp.from", value = var.smtp_from_address },
    { name = "smtp.auth.enabled", value = var.smtp_auth_enabled ? "true" : "false" },
    { name = "smtp.auth.username", value = var.smtp_username },

    # --- Sentry ---
    { name = "sentry.customerName", value = coalesce(var.customer, "Dozuki") },

    # --- Images ---
    { name = "images.app.repository", value = var.image_repository },
    { name = "images.app.tag", value = var.image_tag },
    { name = "images.webnextjs.tag", value = var.nextjs_tag },

    # --- Ingress / Gateway ---
    { name = "ingress.hosts[0].hostname", value = coalesce(var.ingress_hostname, var.dns_domain_name) },
    { name = "gateway.hosts[0].hostname", value = coalesce(var.ingress_hostname, var.dns_domain_name) },
    { name = "gateway.hosts[0].tlsSecretName", value = "tls-secret" },
    # Manual TLS. Supplied certs: chart renders the typed tls-secret (externallyManaged
    # false). Generated self-signed: Terraform renders it (externallyManaged true).
    { name = "tls.enabled", value = local.tls_manual ? "true" : "false" },
    { name = "tls.externallyManaged", value = local.tls_managed_tf ? "true" : "false" },
    { name = "tls.cert", value = local.tls_supplied ? var.tls_cert : "" },

    # --- Webhooks ---
    { name = "webhooks.enabled", value = var.enable_webhooks ? "true" : "false" },

    # --- Object Storage ---
    { name = "objectStorage.kmsKey", value = var.cloud == "aws" ? data.aws_kms_key.s3[0].arn : "" },
    { name = "objectStorage.imagesBucket", value = var.s3_images_bucket },
    { name = "objectStorage.pdfsBucket", value = var.s3_pdfs_bucket },
    { name = "objectStorage.documentsBucket", value = var.s3_documents_bucket },
    { name = "objectStorage.objectsBucket", value = var.s3_objects_bucket },

    # --- Memcached ---
    # In-cluster memcached: use the service FQDN, not the bare name — the app's Hostname
    # type rejects a single-label host ("Invalid hostname"), failing db-migrations and
    # blocking the upgrade. (The bare name resolves via k8s DNS, but the app validates the
    # string format.) ElastiCache supplies a full host already.
    { name = "memcached.host", value = local.memcached_in_cluster ? "dozuki-memcached.${local.k8s_namespace_name}.svc.cluster.local" : var.memcached_cluster_address },

    # --- Vault ---
    { name = "vault.enabled", value = var.cloud == "aws" ? "true" : "false" },
    { name = "vault.address", value = var.vault_address },

    # --- Azure ---
    { name = "azure.enabled", value = var.cloud == "azure" ? "true" : "false" },
    { name = "azure.tenantId", value = var.azure_tenant_id },
    { name = "azure.keyVaultUri", value = var.azure_key_vault_uri },
    { name = "azure.environment", value = var.azure_environment },

    # --- Monitoring ---
    { name = "monitoring.enabled", value = "true" },

    # --- In-cluster services (Azure) ---
    { name = "memcached.enabled", value = local.memcached_in_cluster ? "true" : "false" },
    { name = "objectStorage.endpoint", value = var.cloud == "azure" ? "https://s3.${var.dns_domain_name}" : "" },
    { name = "objectStorage.publicHost", value = var.cloud == "azure" ? "s3.${var.dns_domain_name}" : "" },

    # --- Grafana ---
    { name = "grafana.enabled", value = var.enable_bi ? "true" : "false" },
    { name = "grafana.security.admin_user", value = local.grafana_admin_username },
    { name = "grafana.datasource.host", value = local.db_bi_host },

    # --- Connectivity (sunset planned) ---
    { name = "connectivity.webhook-service.messageBroker.brokerList", value = var.msk_bootstrap_brokers },
    { name = "connectivity.webhook-service.mysql.host", value = local.db_master_host },
    { name = "connectivity.webhook-service.mysql.username", value = local.db_master_username },
    { name = "connectivity.webhook-service.mongo.connectionString", value = "mongodb://dozuki-mongodb/webhooks" },
    { name = "connectivity.integrations-service.messageBroker.brokerList", value = var.msk_bootstrap_brokers },
    { name = "connectivity.integrations-service.mongo.connectionString", value = "mongodb://dozuki-mongodb/integrations" },
    { name = "connectivity.event-service.database.host", value = local.db_master_host },
    { name = "connectivity.event-service.database.username", value = local.db_master_username },
    { name = "connectivity.event-service.messageBroker.brokerList", value = var.msk_bootstrap_brokers },
    { name = "connectivity.event-service.redis.host", value = "dozuki-redis-master" },
    { name = "connectivity.event-service.redis.tls", value = "false" },
    { name = "connectivity.connectors-worker.messageBroker.brokerList", value = var.msk_bootstrap_brokers },
    { name = "connectivity.connectors-worker.redis.host", value = "dozuki-redis-master" },
    { name = "connectivity.connectors-worker.redis.tls", value = "false" },
    ], var.cloud == "azure" ? [
    # --- Operator (azure: pull from GHCR mirror, not ECR) ---
    # The operator subchart reads its OWN imagePullSecrets (it does not honor
    # global.imagePullSecrets), so the GHCR pull secret must be set explicitly.
    { name = "dozuki-operator.image.repository", value = "${var.image_repository}/dozuki-operator" },
    { name = "dozuki-operator.image.tag", value = var.operator_image_tag },
    { name = "dozuki-operator.imagePullSecrets[0].name", value = "ghcr-pull" },
  ] : [])

  set_sensitive = [
    { name = "db.password", value = local.db_master_password },
    { name = "tls.key", value = local.tls_supplied ? var.tls_key : "" },
    { name = "smtp.auth.password", value = var.smtp_password },
    { name = "objectStorage.credentials.accessKey", value = var.cloud == "azure" ? try(random_password.seaweedfs_access_key[0].result, "") : "" },
    { name = "objectStorage.credentials.secretKey", value = var.cloud == "azure" ? try(random_password.seaweedfs_secret_key[0].result, "") : "" },
    { name = "googleTranslate.token", value = var.google_translate_api_token },
    { name = "grafana.security.admin_password", value = local.grafana_admin_password },
    { name = "grafana.datasource.password", value = local.db_bi_password },
  ]

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
