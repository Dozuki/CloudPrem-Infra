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
          image = "imega/mysql-client:10.6.4"
          command = [
            "sh",
            "-c",
            "mysql --host=${local.db_master_host} --user=${local.db_master_username} --password=${local.db_master_password} < /scripts/frontegg-db.sql"
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
