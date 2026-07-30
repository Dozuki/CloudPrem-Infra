# Azure analog of vault_kv_secret_v2 seeding: per-environment + global app
# secrets stored in the customer Key Vault, consumed by ESO (azurekv).
# db credentials are seeded by the physical layer as "database-credentials".

# Azure cannot consume the commercial/Gov Vault global ops path, so retain its
# existing Key Vault-local credential until it has an equivalent shared source.
resource "random_password" "ops_admin" {
  count = var.cloud == "azure" ? 1 : 0

  length  = 24
  special = false
}

# Envoy's BasicAuth filter accepts only htpasswd {SHA}. This is Azure-only:
# Vault-backed stacks consume secret/dozuki/global/ops -> basicAuth directly.
data "external" "ops_htpasswd_hash" {
  count = var.cloud == "azure" ? 1 : 0

  program = ["bash", "-c", <<-EOT
    set -euo pipefail
    password=$(jq -r .password)
    hex=$(printf '%s' "$password" | sha1sum | cut -d' ' -f1)
    hash=$(printf "$(printf '%s' "$hex" | sed 's/../\\x&/g')" | base64)
    jq -n --arg hash "$hash" '{hash: $hash}'
  EOT
  ]

  query = {
    password = random_password.ops_admin[0].result
  }
}

moved {
  from = random_password.ops_admin
  to   = random_password.ops_admin[0]
}

locals {
  azure_kv_secrets = var.cloud == "azure" ? merge({
    # Must be local.memcached_host (the service FQDN), matching the AWS path in vault.tf: ESO
    # syncs this into the app's memcached.json, and the app's Hostname type rejects a bare
    # single-label name with "Invalid hostname". This was hardcoded to "dozuki-memcached".
    cache = jsonencode({
      host = local.memcached_host
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
    surveyjs = jsonencode({
      licenseKey = var.surveyjs_license_key
    })
    rustici = jsonencode({
      password        = var.rustici_password
      managedPassword = var.rustici_managed_password
    })
    # Ops ingress (public Grafana/Alertmanager basic auth): always on, unlike the
    # grafana entry below which is gated by enable_dashboards. AWS stacks instead
    # consume the fleet-global secret/dozuki/global/ops credential.
    ops-auth = jsonencode({
      htpasswd = local.ops_htpasswd
      username = local.ops_user
      password = local.ops_admin_password
    })
    # web-nextjs service JWT signing key. Chart >= 1.9.0 reads this path
    # unconditionally, so the entry must exist even while the value is empty.
    # AWS twin: secret/dozuki/global/nextjs, synced from 1Password by
    # infra-tf's vault-config.
    nextjs = jsonencode({
      privateKey = var.nextjs_service_jwt_private_key
    })
    # Monolith-side service JWT validation key. Chart >= 1.12.0 reads this
    # path unconditionally (same must-exist contract as nextjs above), so the
    # entry exists even while empty; the chart composes the actual
    # service-jwt.json file. AWS twin: secret/dozuki/global/service-jwt.
    service-jwt = jsonencode({
      validationKey = var.service_jwt_validation_key
    })
    # Zendesk JWT signing key. Same must-exist contract as nextjs/service-jwt
    # above (chart >= 1.13.0); the chart composes zendesk.json and hardcodes
    # isEnabled true. AWS twin: secret/dozuki/global/zendesk.
    zendesk = jsonencode({
      jwtSigningKey = var.zendesk_jwt_signing_key
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
