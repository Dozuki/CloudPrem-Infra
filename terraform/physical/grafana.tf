module "grafana_ssl_cert" {
  # If BI is enabled and we are NOT using the replicated ssl cert then create one.
  count = var.enable_bi ? !var.grafana_use_replicated_ssl ? 1 : 0 : 0

  source      = "./modules/acm"
  environment = var.environment
  identifier  = var.identifier

  cert_common_name = local.grafana_ssl_cert_cn
  namespace        = "grafana"
}