# Azure analog of vault_kv_secret_v2 seeding: per-environment + global app
# secrets stored in the customer Key Vault, consumed by ESO (azurekv).
# db credentials are seeded by the physical layer as "database-credentials".

locals {
  azure_kv_secrets = var.cloud == "azure" ? merge({
    cache = jsonencode({
      host = "dozuki-memcached"
    })
    google-translate = jsonencode({
      token = var.google_translate_api_token
    })
    smtp = jsonencode({
      password = var.smtp_password
    })
    sentry = jsonencode({
      dsn = var.sentry_dsn
    })
    frontegg = jsonencode({
      clientId = var.frontegg_client_id
      apiToken = var.frontegg_api_token
    })
    surveyjs = jsonencode({
      licenseKey = var.surveyjs_license_key
    })
    rustici = jsonencode({
      password        = var.rustici_password
      managedPassword = var.rustici_managed_password
    })
    # Ops ingress (public Grafana/Alertmanager basic auth): always on, unlike the
    # grafana entry below which is gated by enable_dashboards. AWS twin in vault.tf's
    # vault_kv_secret_v2.ops_auth.
    ops-auth = jsonencode({
      htpasswd = local.ops_htpasswd
      username = local.ops_user
      password = local.ops_admin_password
    })
    }, var.enable_dashboards ? {
    # Keys match the chart's ESO remoteRef properties (see vault.tf's
    # vault_kv_secret_v2.grafana for the AWS twin of this same entry).
    grafana = jsonencode({
      secret        = local.dashboards_jwt_secret
      adminUser     = local.dashboards_admin_username
      adminPassword = local.dashboards_admin_password
    })
  } : {}) : {}
}

resource "azurerm_key_vault_secret" "app" {
  for_each = local.azure_kv_secrets
  provider = azurerm.main["azure"]

  name         = each.key
  key_vault_id = var.azure_key_vault_id
  content_type = "application/json"
  value        = each.value
}
