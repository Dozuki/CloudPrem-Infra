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

module "grafana_ssl_cert" {
  # If BI is enabled and we are NOT using the replicated ssl cert then create one.
  count = var.enable_bi ? !var.grafana_use_replicated_ssl ? 1 : 0 : 0

  source      = "../common/acm"
  environment = var.environment
  identifier  = var.identifier

  cert_common_name = local.grafana_ssl_cert_cn
  namespace        = "grafana"
}

resource "kubernetes_secret" "grafana_ssl" {
  count = var.enable_bi ? !var.grafana_use_replicated_ssl ? 1 : 0 : 0

  metadata {
    name      = "grafana-ssl"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }

  data = {
    "onprem.key" = module.grafana_ssl_cert[0].ssm_server_key.value
    "onprem.crt" = module.grafana_ssl_cert[0].ssm_server_cert.value
  }
}

resource "kubernetes_secret" "grafana_config" {

  metadata {
    name      = "grafana-config"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }

  data = {
    GF_SERVER_PROTOCOL     = "https"
    GF_SERVER_CERT_FILE    = "/etc/secrets/onprem.crt"
    GF_SERVER_CERT_KEY     = "/etc/secrets/onprem.key"
    GF_USERS_DEFAULT_THEME = "light"
  }
}

resource "random_password" "grafana_admin" {
  count = var.enable_bi ? 1 : 0

  length  = 16
  special = false
}

resource "helm_release" "grafana" {
  count = var.enable_bi ? 1 : 0

  depends_on = [kubernetes_secret.grafana_ssl, kubernetes_secret.grafana_config, local_file.replicated_install]

  name  = "grafana"
  chart = "${path.module}/charts/grafana"

  namespace = kubernetes_namespace.kots_app.metadata[0].name

  reuse_values = true

  values = [
    templatefile("${path.module}/static/grafana_values.yml", {
      ssl_secret_name   = local.grafana_ssl_secret_name
      database_hostname = local.db_bi_host
      database_password = local.db_bi_password
    })
  ]

  set_sensitive {
    name  = "adminPassword"
    value = random_password.grafana_admin[0].result
  }
}