resource "kubernetes_namespace" "app" {
  depends_on = [helm_release.ebs_csi_driver]
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

resource "helm_release" "metrics_server" {
  name  = "metrics-server"
  chart = "charts/metrics-server"
}

resource "helm_release" "adot_exporter" {
  depends_on = [helm_release.metrics_server]

  name  = "adot-exporter-for-eks-on-ec2"
  chart = "${path.module}/charts/adot-exporter-for-eks-on-ec2"

  set {
    name  = "clusterName"
    value = var.eks_cluster_id
  }

  set {
    name  = "awsRegion"
    value = data.aws_region.current.name
  }

  set {
    name  = "adotCollector.daemonSet.service.metrics.receivers"
    value = "{awscontainerinsightreceiver}"
  }
  set {
    name  = "adotCollector.daemonSet.service.metrics.exporters"
    value = "{awsemf}"
  }
}

resource "helm_release" "fluent_bit_log_exporter" {
  depends_on = [helm_release.adot_exporter]

  chart = "${path.module}/charts/aws-for-fluent-bit"
  name  = "aws-for-fluent-bit"

  namespace = "amazon-metrics"

  set {
    name  = "cloudWatchLogs.region"
    value = data.aws_region.current.name
  }

  set {
    name  = "global.namespaceOverride"
    value = "amazon-metrics"
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

resource "helm_release" "ebs_csi_driver" {
  name  = "ebs-csi-driver"
  chart = "${path.module}/charts/aws-ebs-csi-driver"

  values = [
    file("static/ebs-csi-driver-values.yaml")
  ]

  namespace = "kube-system"

  wait = true
}

resource "helm_release" "app" {
  depends_on = [helm_release.cert_manager]

  name      = "dozuki"
  namespace = kubernetes_namespace.app.metadata[0].name

  chart = "${path.module}/charts/dozuki/chart"

  wait              = false
  dependency_update = true

  values = [jsonencode({
    hostname       = var.dns_domain_name
    dns_validation = local.dns_validation
    customer       = coalesce(var.customer, "Dozuki")
    environment    = var.environment

    aws = {
      region    = data.aws_region.current.name
      accountId = data.aws_caller_identity.current.account_id
      enabled   = true
    }

    db = {
      host      = local.db_master_host
      user      = local.db_master_username
      rdsCaCert = base64encode(file(local.ca_cert_pem_file))
    }

    smtp = {
      enabled = try(local.secret_values["smtp_enabled"], false)
      host    = try(local.secret_values["smtp_host"], "")
      from    = try(local.secret_values["smtp_from_address"], "")
      auth = {
        enabled  = try(local.secret_values["smtp_auth_enabled"], false)
        username = try(local.secret_values["smtp_username"], "")
      }
    }

    sentry = {
      customerName = coalesce(var.customer, "Dozuki")
    }

    images = {
      app = {
        repository = try(local.secret_values["image_repository"], "")
        tag        = try(local.secret_values["image_tag"], "")
      }
      webnextjs = {
        tag = try(local.secret_values["nextjs_tag"], "")
      }
    }

    ingress = {
      hosts = [{
        hostname = coalesce(var.ingress_hostname, var.dns_domain_name)
      }]
    }

    webhooks = {
      enabled = var.enable_webhooks
    }

    objectStorage = {
      kmsKey          = data.aws_kms_key.s3.arn
      imagesBucket    = var.s3_images_bucket
      pdfsBucket      = var.s3_pdfs_bucket
      documentsBucket = var.s3_documents_bucket
      objectsBucket   = var.s3_objects_bucket
    }

    memcached = {
      host = var.memcached_cluster_address
    }

    grafana = {
      enabled = var.enable_bi
      security = {
        admin_user = local.grafana_admin_username
      }
      datasource = {
        host = local.db_bi_host
      }
    }

    secrets = [
      {
        name    = "grafana-common-config"
        enabled = var.enable_bi
        stringData = {
          GRAFANA_SUBPATH                         = "dashboards"
          GF_SERVER_SERVE_FROM_SUBPATH            = "true"
          GF_USERS_DEFAULT_THEME                  = "light"
          GF_DATABASE_TYPE                        = "mysql"
          GF_DATABASE_HOST                        = local.db_master_host
          GF_DATABASE_USER                        = local.db_master_username
          GF_DATABASE_PASSWORD                    = local.db_master_password
          GF_ANALYTICS_REPORTING_ENABLED          = "false"
          GF_ANALYTICS_CHECK_FOR_UPDATES          = "false"
          GF_METRICS_ENABLED                      = "false"
          GF_SECURITY_ADMIN_PASSWORD              = local.grafana_admin_password
          GF_SECURITY_ADMIN_USER                  = local.grafana_admin_username
          GF_SECURITY_COOKIE_SECURE               = "true"
          GF_SECURITY_DATA_SOURCE_PROXY_WHITELIST = "1.1.1.1:1"
          GF_SECURITY_COOKIE_SAMESITE             = "strict"
          GF_SECURITY_X_XSS_PROTECTION            = "true"
          GF_SMTP_ENABLED                         = local.sensitive_helm_values.grafana_smtp_enabled
          GF_SMTP_HOST                            = local.sensitive_helm_values.grafana_smtp_host
          GF_SMTP_USER                            = local.sensitive_helm_values.grafana_smtp_user
          GF_SMTP_PASSWORD                        = local.sensitive_helm_values.grafana_smtp_password
          GF_SMTP_FROM_ADDRESS                    = local.sensitive_helm_values.grafana_smtp_from_address
          GF_SMTP_FROM_NAME                       = "Dozuki Grafana Dashboard"
          GF_SMTP_STARTTLS_POLICY                 = local.sensitive_helm_values.grafana_smtp_starttls
        }
      },
      {
        name    = "ops-basic-auth"
        enabled = true
        stringData = {
          auth = local.sensitive_helm_values.ops_basic_auth
        }
      }
    ]

    connectivity = {
      frontegg = {
        images = {}
      }
      "webhook-service" = {
        messageBroker = { brokerList = var.msk_bootstrap_brokers }
        mysql         = { host = local.db_master_host, username = local.db_master_username }
        mongo         = { connectionString = "mongodb://dozuki-mongodb/webhooks" }
      }
      "integrations-service" = {
        messageBroker = { brokerList = var.msk_bootstrap_brokers }
        mongo         = { connectionString = "mongodb://dozuki-mongodb/integrations" }
        frontegg      = { slack = { encryptionKey = "dummyval" } }
      }
      "event-service" = {
        database      = { host = local.db_master_host, username = local.db_master_username }
        messageBroker = { brokerList = var.msk_bootstrap_brokers }
        redis         = { host = "dozuki-redis-master", tls = "false" }
        frontegg      = { sync = { enabled = "false" }, authenticationUrl = "https://api.frontegg.com/auth/vendor" }
      }
      "connectors-worker" = {
        messageBroker = { brokerList = var.msk_bootstrap_brokers }
        redis         = { host = "dozuki-redis-master", tls = "false" }
        frontegg      = { channels = "slack", emails = { provider = "sendgrid", sendgrid = { apiKey = "dummyval" } } }
      }
    }

    "kube-prometheus-stack" = {
      grafana = {
        defaultDashboardsTimezone = "America/Los_Angeles"
      }
    }

    rustici = {}
  })]

  set_sensitive {
    name  = "db.password"
    value = local.db_master_password
  }
  set_sensitive {
    name  = "smtp.auth.password"
    value = local.sensitive_helm_values.smtp_password
  }
  set_sensitive {
    name  = "sentry.dsn"
    value = local.sensitive_helm_values.sentry_dsn
  }
  set_sensitive {
    name  = "frontegg.clientId"
    value = local.sensitive_helm_values.frontegg_client_id
  }
  set_sensitive {
    name  = "frontegg.apiToken"
    value = local.sensitive_helm_values.frontegg_api_token
  }
  set_sensitive {
    name  = "surveyjs.licenseKey"
    value = local.sensitive_helm_values.surveyjs_license_key
  }
  set_sensitive {
    name  = "googleTranslate.token"
    value = local.sensitive_helm_values.google_translate_token
  }
  set_sensitive {
    name  = "rustici.password"
    value = local.sensitive_helm_values.rustici_password
  }
  set_sensitive {
    name  = "rustici.managedPassword"
    value = local.sensitive_helm_values.rustici_managed_password
  }
  set_sensitive {
    name  = "connectivity.frontegg.images.username"
    value = local.sensitive_helm_values.frontegg_docker_username
  }
  set_sensitive {
    name  = "connectivity.frontegg.images.password"
    value = local.sensitive_helm_values.frontegg_docker_password
  }
  set_sensitive {
    name  = "connectivity.api-gateway.frontegg.authenticationPublicKey"
    value = local.sensitive_helm_values.frontegg_auth_pubkey
  }
  set_sensitive {
    name  = "connectivity.webhook-service.mysql.password"
    value = local.db_master_password
  }
  set_sensitive {
    name  = "connectivity.event-service.database.password"
    value = local.db_master_password
  }
  set_sensitive {
    name  = "connectivity.event-service.frontegg.clientId"
    value = local.sensitive_helm_values.frontegg_client_id
  }
  set_sensitive {
    name  = "connectivity.event-service.frontegg.apiKey"
    value = local.sensitive_helm_values.frontegg_api_token
  }
  set_sensitive {
    name  = "grafana.security.admin_password"
    value = local.grafana_admin_password
  }
  set_sensitive {
    name  = "grafana.datasource.password"
    value = local.db_bi_password
  }
  set_sensitive {
    name  = "kube-prometheus-stack.grafana.adminPassword"
    value = local.sensitive_helm_values.infra_auth_password
  }
}