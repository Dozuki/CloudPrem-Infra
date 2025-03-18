resource "kubernetes_job" "dms_start" {
  count = var.dms_enabled ? 1 : 0

  depends_on = [kubernetes_cluster_role_binding.dozuki_list_role_binding]

  metadata {
    name      = "dms-start"
    namespace = kubernetes_namespace.app.metadata[0].name
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "dms-start"
          image = "bearengineer/awscli-kubectl:latest"
          command = [
            "/bin/sh",
            "-c",
            "kubectl wait deploy/app-deployment --for condition=available --timeout=1200s && aws dms start-replication-task --start-replication-task-type start-replication --replication-task-arn ${var.dms_task_arn} --region ${data.aws_region.current.name}"
          ]
        }
        restart_policy = "Never"
      }
    }
    completions = 1
  }
  wait_for_completion = false

  timeouts {
    create = "20m"
  }
}

resource "kubernetes_config_map" "grafana_create_db_script" {
  count = var.enable_bi ? 1 : 0
  metadata {
    name      = "grafana-create-db-script"
    namespace = kubernetes_namespace.app.metadata[0].name
  }

  data = {
    "grafana-db.sql" = file("static/grafana-db.sql")
  }
}

resource "kubernetes_job" "grafana_db_create" {
  count = var.enable_bi ? 1 : 0

  metadata {
    name      = "grafana-db-create"
    namespace = kubernetes_namespace.app.metadata[0].name
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "grafana-db-create"
          image = "imega/mysql-client:10.6.4"
          env {
            name = "MYSQL_HOST"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.dozuki_infra_credentials.metadata[0].name
                key  = "master_host"
              }
            }
          }
          env {
            name = "MYSQL_USER"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.dozuki_infra_credentials.metadata[0].name
                key  = "master_user"
              }
            }
          }
          env {
            name = "MYSQL_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.dozuki_infra_credentials.metadata[0].name
                key  = "master_password"
              }
            }
          }
          command = [
            "sh",
            "-c",
            "mysql --host=$MYSQL_HOST --user=$MYSQL_USER --password=$MYSQL_PASSWORD < /scripts/grafana-db.sql"
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
            name = kubernetes_config_map.grafana_create_db_script[0].metadata[0].name
          }
        }
        restart_policy = "OnFailure"
      }
    }
    backoff_limit = 50
  }
  wait_for_completion = true
}

resource "random_password" "grafana_admin" {
  count = var.enable_bi ? 1 : 0

  length  = 16
  special = false
}