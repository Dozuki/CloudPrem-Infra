data "aws_ssm_parameter" "grafana_ssl_cert" {
  count = var.enable_bi ? !var.grafana_use_replicated_ssl ? 1 : 0 : 0

  name = var.grafana_ssl_server_cert_parameter
}
data "aws_ssm_parameter" "grafana_ssl_key" {
  count = var.enable_bi ? !var.grafana_use_replicated_ssl ? 1 : 0 : 0

  name = var.grafana_ssl_server_key_parameter
}

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

resource "kubernetes_secret" "grafana_ssl" {
  count = var.enable_bi ? !var.grafana_use_replicated_ssl ? 1 : 0 : 0

  metadata {
    name      = "grafana-ssl"
    namespace = kubernetes_namespace.kots_app.metadata[0].name
  }

  data = {
    "onprem.key" = data.aws_ssm_parameter.grafana_ssl_key[0].value
    "onprem.crt" = data.aws_ssm_parameter.grafana_ssl_cert[0].value
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