resource "kubernetes_job" "dms_start" {
  count = var.enable_bi ? 1 : 0

  depends_on = [helm_release.replicated]

  metadata {
    name = "dms-start"
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
            "kubectl -n ${coalesce([for i, v in data.kubernetes_all_namespaces.allns.namespaces : try(regexall("replicated\\-.*", v)[0], "")]...)} wait deploy/app-deployment --for condition=available --timeout=1200s && aws dms start-replication-task --start-replication-task-type start-replication --replication-task-arn ${var.dms_task_arn} --region ${data.aws_region.current.name}"
          ]
        }
        restart_policy = "Never"
      }
    }
    backoff_limit = 1
    completions   = 1
  }
  wait_for_completion = false

  timeouts {
    create = "20m"
  }
}

module "ssl_cert" {
  source      = "../common/acm"
  environment = var.environment
  identifier  = var.identifier

  cert_common_name = var.nlb_dns_name
  namespace        = "grafana"
}

resource "kubernetes_secret" "grafana_ssl" {
  metadata {
    name = "grafana-ssl"
  }

  data = {
    "server.key" = module.ssl_cert.ssm_server_key.value
    "server.crt" = module.ssl_cert.ssm_server_cert.value
  }
}

resource "kubernetes_secret" "grafana_config" {
  metadata {
    name = "grafana-config"
  }

  data = {
    GF_SERVER_PROTOCOL     = "https"
    GF_SERVER_CERT_FILE    = "/etc/secrets/server.crt"
    GF_SERVER_CERT_KEY     = "/etc/secrets/server.key"
    GF_USERS_DEFAULT_THEME = "light"
  }
}

resource "random_password" "grafana_admin" {
  count = var.enable_bi ? 1 : 0

  length  = 16
  special = true
}

resource "helm_release" "grafana" {
  count = var.enable_bi ? 1 : 0

  depends_on = [kubernetes_secret.grafana_ssl, kubernetes_secret.grafana_config]

  name  = "grafana"
  chart = "${path.module}/charts/grafana"

  namespace = "default"

  reuse_values = true

  values = [
    templatefile("${path.module}/static/grafana_values.yml", {
      database_hostname = local.db_bi_host
      database_password = local.db_bi_password
    })
  ]

  set_sensitive {
    name  = "adminPassword"
    value = random_password.grafana_admin[0].result
  }
}