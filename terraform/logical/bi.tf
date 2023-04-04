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
    GF_DATABASE_HOST                        = local.db_bi_host
    GF_DATABASE_USER                        = local.db_bi_username
    GF_DATABASE_PASSWORD                    = local.db_bi_password
    GF_ANALYTICS_REPORTING_ENABLED          = false
    GF_ANALYTICS_CHECK_FOR_UPDATES          = false
    GF_METRICS_ENABLED                      = false
    GF_SECURITY_COOKIE_SECURE               = true
    GF_SECURITY_DATA_SOURCE_PROXY_WHITELIST = "1.1.1.1:1" #Disable all data source proxying
    GF_SECURITY_COOKIE_SAMESITE             = "strict"
    GF_SECURITY_X_XSS_PROTECTION            = true
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
          image = "imega/mysql-client"
          command = [
            "mysql",
            "--host=${local.db_bi_host}",
            "--user=${local.db_bi_username}",
            "--password=${local.db_bi_password}",
            "--execute=${file("static/grafana-db.sql")}"
          ]
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

resource "helm_release" "grafana" {
  count = var.enable_bi ? 1 : 0

  depends_on = [kubernetes_secret.grafana_config, local_file.replicated_install, kubernetes_job.grafana_db_create]

  name  = "grafana"
  chart = "${path.module}/charts/grafana"

  namespace = kubernetes_namespace.kots_app.metadata[0].name

  reuse_values = true

  values = [
    templatefile("${path.module}/static/grafana_values.yml", {
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