# Vault secret seeding — writes per-environment secrets that Terraform manages
# and reads system-wide secrets pre-seeded in vault-infrastructure.
#
# These resources only apply when vault is enabled. When vault is disabled,
# secrets flow through the devops/app/config Secrets Manager secret instead.

locals {
  vault_tenant     = coalesce(var.customer, "dozuki")
  vault_env_prefix = "${local.vault_tenant}/${var.environment}"
}

# --- Per-environment secrets (seeded by Terraform) --- #

resource "vault_kv_secret_v2" "db" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "${local.vault_env_prefix}/db"

  data_json = jsonencode({
    host     = local.db_master_host
    username = local.db_master_username
    password = local.db_master_password
  })
}

resource "vault_kv_secret_v2" "bi" {
  count = var.enable_vault && var.enable_bi ? 1 : 0
  mount = "secret"
  name  = "${local.vault_env_prefix}/bi"

  data_json = jsonencode({
    host     = local.db_bi_host
    password = local.db_bi_password
  })
}

resource "vault_kv_secret_v2" "cache" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "${local.vault_env_prefix}/cache"

  data_json = jsonencode({
    host = var.memcached_cluster_address
  })
}

resource "vault_kv_secret_v2" "google_translate" {
  count = var.enable_vault && var.google_translate_api_token != "" ? 1 : 0
  mount = "secret"
  name  = "${local.vault_env_prefix}/google-translate"

  data_json = jsonencode({
    token = var.google_translate_api_token
  })
}

resource "vault_kv_secret_v2" "smtp" {
  count = var.enable_vault && var.smtp_password != "" ? 1 : 0
  mount = "secret"
  name  = "${local.vault_env_prefix}/smtp"

  data_json = jsonencode({
    password = var.smtp_password
  })
}

# --- System-wide secrets (read from Vault, pre-seeded by vault-infrastructure) --- #

data "vault_kv_secret_v2" "global_sentry" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/sentry"
}

data "vault_kv_secret_v2" "global_frontegg" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/frontegg"
}

data "vault_kv_secret_v2" "global_surveyjs" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/surveyjs"
}

data "vault_kv_secret_v2" "global_rustici" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/rustici"
}

data "vault_kv_secret_v2" "global_ops" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/ops"
}

data "vault_kv_secret_v2" "global_slack" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/slack"
}

# --- Vault-sourced config values (replace devops/app/config SM when vault enabled) --- #

locals {
  vault_config_values = var.enable_vault ? {
    sentry_dsn                = { value = data.vault_kv_secret_v2.global_sentry[0].data["dsn"] }
    frontegg_client_id        = { value = data.vault_kv_secret_v2.global_frontegg[0].data["clientId"] }
    frontegg_api_token        = { value = data.vault_kv_secret_v2.global_frontegg[0].data["apiToken"] }
    frontegg_docker_username  = { value = data.vault_kv_secret_v2.global_frontegg[0].data["dockerUsername"] }
    frontegg_docker_password  = { value = data.vault_kv_secret_v2.global_frontegg[0].data["dockerPassword"] }
    frontegg_auth_pubkey      = { value = data.vault_kv_secret_v2.global_frontegg[0].data["authPubkey"] }
    surveyjs_license_key      = { value = data.vault_kv_secret_v2.global_surveyjs[0].data["licenseKey"] }
    rustici_password          = { value = data.vault_kv_secret_v2.global_rustici[0].data["password"] }
    rustici_managed_password  = { value = data.vault_kv_secret_v2.global_rustici[0].data["managedPassword"] }
    ops_basic_auth            = { value = data.vault_kv_secret_v2.global_ops[0].data["basicAuth"] }
    infra_auth_password       = { value = data.vault_kv_secret_v2.global_ops[0].data["infraAuthPassword"] }
    slack_webhook_url         = { value = data.vault_kv_secret_v2.global_slack[0].data["webhookUrl"] }
    grafana_smtp_enabled      = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpEnabled"], "false") }
    grafana_smtp_host         = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpHost"], "") }
    grafana_smtp_user         = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpUser"], "") }
    grafana_smtp_password     = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpPassword"], "") }
    grafana_smtp_from_address = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpFromAddress"], "") }
    grafana_smtp_starttls     = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpStarttls"], "OpportunisticStartTLS") }
  } : {}
}
