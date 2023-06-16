
data "kubernetes_secret" "frontegg" {
  count = var.enable_webhooks ? 1 : 0

  depends_on = [local_file.replicated_install]

  metadata {
    name      = "frontegg-credentials"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }
}
resource "kubernetes_job" "wait_for_app" {
  count = var.enable_webhooks ? 1 : 0

  depends_on = [local_file.replicated_install, kubernetes_cluster_role_binding.dozuki_list_role_binding]

  metadata {
    name      = "wait-for-app"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "wait-for-app"
          image = "bearengineer/awscli-kubectl:latest"
          command = [
            "/bin/sh",
            "-c",
            "kubectl wait deploy/app-deployment --for condition=available --timeout=1200s"
          ]
        }
        restart_policy = "Never"
      }
    }
    backoff_limit = 1
    completions   = 1
  }
  wait_for_completion = true

  timeouts {
    create = "20m"
  }
}
resource "kubernetes_job" "frontegg_database_create" {
  count = var.enable_webhooks ? 1 : 0

  metadata {
    name      = "frontegg-db-update"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "frontegg-db-update"
          image = "imega/mysql-client:10.6.4"
          command = [
            "mysql",
            "--host=${local.db_master_host}",
            "--user=${local.db_master_username}",
            "--password=${local.db_master_password}",
            "--execute=${file("static/frontegg-db.sql")}"
          ]
        }
        restart_policy = "Never"
      }
    }
    backoff_limit = 10
  }
  wait_for_completion = true
}
resource "kubernetes_job" "sites_config_update" {
  count = var.enable_webhooks ? 1 : 0

  depends_on = [kubernetes_job.wait_for_app]

  metadata {
    name      = "sites-config-update"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "sites-config-update"
          image = "imega/mysql-client:10.6.4"
          command = [
            "mysql",
            "--host=${local.db_master_host}",
            "--user=${local.db_master_username}",
            "--password=${local.db_master_password}",
            "--execute=${file("static/sites-db.sql")}"
          ]
        }
        restart_policy = "OnFailure"
      }
    }
    backoff_limit = 50
  }
  wait_for_completion = true
}

resource "helm_release" "mongodb" {
  count = var.enable_webhooks ? 1 : 0

  name      = "frontegg-documents"
  chart     = "charts/mongodb"
  namespace = kubernetes_namespace.kots_app.metadata[0].name

  set {
    name  = "auth.enabled"
    value = "false"
  }
}

resource "helm_release" "redis" {
  count = var.enable_webhooks ? 1 : 0

  name      = "frontegg-kvstore"
  chart     = "charts/redis"
  namespace = kubernetes_namespace.kots_app.metadata[0].name

  set {
    name  = "auth.enabled"
    value = "false"
  }
  set {
    name  = "tls.authClients"
    value = "false"
  }
  set {
    name  = "architecture"
    value = "standalone"
  }
}


resource "helm_release" "frontegg" {
  count = var.enable_webhooks ? 1 : 0

  depends_on = [
    helm_release.mongodb,
    helm_release.redis,
    kubernetes_job.frontegg_database_create
  ]

  name  = "frontegg"
  chart = "charts/connectivity"

  namespace = kubernetes_namespace.kots_app.metadata[0].name

  reuse_values = true

  values = [
    file("static/webhooks_values.yml")
  ]

  // - Frontegg Auth - //
  set_sensitive {
    name  = "event-service.frontegg.clientId"
    value = local.frontegg_clientid
  }
  set_sensitive {
    name  = "event-service.frontegg.apiKey"
    value = local.frontegg_apikey
  }
  set_sensitive {
    name  = "api-gateway.frontegg.authenticationPublicKey"
    value = local.frontegg_pub_key
  }
  set_sensitive {
    name  = "frontegg.images.username"
    value = local.frontegg_username
  }
  set_sensitive {
    name  = "frontegg.images.password"
    value = local.frontegg_password
  }

  // - Kafka - //
  set {
    name  = "webhook-service.messageBroker.brokerList"
    value = var.msk_bootstrap_brokers
  }
  set {
    name  = "event-service.messageBroker.brokerList"
    value = var.msk_bootstrap_brokers
  }
  set {
    name  = "integrations-service.messageBroker.brokerList"
    value = var.msk_bootstrap_brokers
  }
  set {
    name  = "connectors-worker.messageBroker.brokerList"
    value = var.msk_bootstrap_brokers
  }

  // - MySQL - //
  set {
    name  = "webhook-service.mysql.host"
    value = local.db_master_host
  }
  set {
    name  = "event-service.database.host"
    value = local.db_master_host
  }
  set {
    name  = "webhook-service.mysql.username"
    value = local.db_master_username
  }
  set {
    name  = "event-service.database.username"
    value = local.db_master_username
  }
  set_sensitive {
    name  = "webhook-service.mysql.password"
    value = local.db_master_password
  }
  set_sensitive {
    name  = "event-service.database.password"
    value = local.db_master_password
  }

}
