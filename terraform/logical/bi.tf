resource "kubernetes_job" "dms_start" {
  count = var.enable_bi ? 1 : 0

  depends_on = [local_file.replicated_install, kubernetes_role_binding.dozuki_list_role_binding]

  metadata {
    name      = "dms-start"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
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

resource "kubernetes_secret" "grafana_config" {
  count = var.enable_bi ? 1 : 0

  metadata {
    name      = "grafana-config"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }

  data = {
    GF_SERVER_ROOT_URL                      = local.grafana_url
    GF_SERVER_SERVE_FROM_SUBPATH            = true
    GF_USERS_DEFAULT_THEME                  = "light"
    GF_DATABASE_TYPE                        = "mysql"
    GF_DATABASE_HOST                        = local.db_master_host
    GF_DATABASE_USER                        = local.db_master_username
    GF_DATABASE_PASSWORD                    = local.db_master_password
    GF_ANALYTICS_REPORTING_ENABLED          = false
    GF_ANALYTICS_CHECK_FOR_UPDATES          = false
    GF_METRICS_ENABLED                      = false
    GF_SECURITY_COOKIE_SECURE               = true
    GF_SECURITY_DATA_SOURCE_PROXY_WHITELIST = "1.1.1.1:1" #Disable all data source proxying
    GF_SECURITY_COOKIE_SAMESITE             = "strict"
    GF_SECURITY_X_XSS_PROTECTION            = true
  }
}

resource "kubernetes_config_map" "grafana_create_db_script" {
  count = var.enable_bi ? 1 : 0
  metadata {
    name      = "grafana-create-db-script"
    namespace = local.k8s_namespace_name
  }

  data = {
    "grafana-db.sql" = file("static/grafana-db.sql")
  }
}


resource "kubernetes_secret" "grafana_mysql_credentials" {
  count = var.enable_bi ? 1 : 0

  metadata {
    name      = "grafana-mysql-credentials"
    namespace = local.k8s_namespace_name
  }
  type = "Opaque"

  data = {
    host     = local.db_master_host
    user     = local.db_master_username
    password = local.db_master_password
  }
}

resource "kubernetes_job" "grafana_db_create" {
  count = var.enable_bi ? 1 : 0

  metadata {
    name      = "grafana-db-create"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
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
                name = kubernetes_secret.grafana_mysql_credentials[0].metadata[0].name
                key  = "host"
              }
            }
          }
          env {
            name = "MYSQL_USER"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.grafana_mysql_credentials[0].metadata[0].name
                key  = "user"
              }
            }
          }
          env {
            name = "MYSQL_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.grafana_mysql_credentials[0].metadata[0].name
                key  = "password"
              }
            }
          }
          command = [
            "sh",
            "-c",
            "mysql --host=$MYSQL_HOST --user=$MYSQL_USER --password=$MYSQL_PASSWORD --execute=\"$(cat /scripts/grafana-db.sql)\""
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

resource "kubernetes_job" "wait_for_ingress" {
  count = var.enable_bi ? 1 : 0

  depends_on = [local_file.replicated_install, kubernetes_role_binding.dozuki_list_role_binding]

  metadata {
    name      = "wait-for-ingress"
    namespace = "ingress-nginx"
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "wait-for-ingress"
          image = "bearengineer/awscli-kubectl:latest"
          command = [
            "/bin/sh",
            "-c",
            <<EOT
            while true; do
              desired_number=$(kubectl get daemonset -l app.kubernetes.io/name=ingress-nginx -o jsonpath='{.items[0].status.desiredNumberScheduled}')
              number_ready=$(kubectl get daemonset -l app.kubernetes.io/name=ingress-nginx -o jsonpath='{.items[0].status.numberReady}')
              if [[ "$desired_number" == "$number_ready" ]]; then
                break
              else
                echo "Waiting for Ingress controller DaemonSet to be ready..."
              sleep 5
              fi
            done
            EOT
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

resource "helm_release" "grafana" {
  count = var.enable_bi ? 1 : 0

  depends_on = [kubernetes_secret.grafana_config, local_file.replicated_install, kubernetes_job.grafana_db_create, kubernetes_job.wait_for_ingress]

  name  = "grafana"
  chart = "charts/grafana"

  namespace = kubernetes_namespace.kots_app.metadata[0].name

  reuse_values = true

  values = [
    templatefile("static/grafana_values.yml", {
      admin_user        = local.grafana_admin_username
      database_hostname = local.db_bi_host
      database_password = local.db_bi_password
      hostname          = var.dns_domain_name
    })
  ]

  set_sensitive {
    name  = "adminPassword"
    value = random_password.grafana_admin[0].result
  }
}