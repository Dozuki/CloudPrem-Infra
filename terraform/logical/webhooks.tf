resource "kubernetes_secret_v1" "frontegg_db_credentials" {
  count = var.enable_webhooks ? 1 : 0

  metadata {
    name      = "frontegg-db-credentials"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
  type = "Opaque"

  data = {
    host     = local.db_master_host
    username = local.db_master_username
    password = local.db_master_password
  }
}

resource "kubernetes_config_map_v1" "frontegg_db_script" {
  count = var.enable_webhooks ? 1 : 0

  metadata {
    name      = "frontegg-db-script"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }

  data = {
    "frontegg-db.sql" = file("static/frontegg-db.sql")
  }
}

resource "kubernetes_job_v1" "frontegg_db_create" {
  count      = var.enable_webhooks ? 1 : 0
  depends_on = [helm_release.app]

  metadata {
    name      = "frontegg-db-create"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "frontegg-db-create"
          image = "mysql:9.3"
          env {
            name = "MYSQL_HOST"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.frontegg_db_credentials[0].metadata[0].name
                key  = "host"
              }
            }
          }
          env {
            name = "MYSQL_USER"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.frontegg_db_credentials[0].metadata[0].name
                key  = "username"
              }
            }
          }
          env {
            name = "MYSQL_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.frontegg_db_credentials[0].metadata[0].name
                key  = "password"
              }
            }
          }
          command = [
            "sh",
            "-c",
            "mysql --host=$MYSQL_HOST --user=$MYSQL_USER --password=$MYSQL_PASSWORD < /scripts/frontegg-db.sql"
          ]
          volume_mount {
            name       = "scripts"
            mount_path = "/scripts"
            read_only  = true
          }
        }
        volume {
          name = "scripts"
          config_map {
            name = kubernetes_config_map_v1.frontegg_db_script[0].metadata[0].name
          }
        }
        restart_policy = "Never"
      }
    }
    backoff_limit = 10
  }
  wait_for_completion = true

  timeouts {
    create = "5m"
  }
}
