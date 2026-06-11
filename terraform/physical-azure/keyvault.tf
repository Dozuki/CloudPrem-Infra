# Key Vault names: 3-24 chars, globally unique.
# "kv-" + identifier_compact (max 15) + "-" + 4-char suffix = max 23.
# v1 intentionally leaves the vault endpoint publicly reachable: deploys are
# operator-driven from outside the VNet and ESO reaches it from the cluster.
# Follow-up: private endpoint + public_network_access_enabled = false.
resource "azurerm_key_vault" "this" {
  name                       = "kv-${local.identifier_compact}-${random_string.suffix.result}"
  location                   = azurerm_resource_group.this.location
  resource_group_name        = azurerm_resource_group.this.name
  tenant_id                  = data.azurerm_client_config.current.tenant_id
  sku_name                   = "standard"
  rbac_authorization_enabled = true
  purge_protection_enabled   = var.protect_resources
  soft_delete_retention_days = 30
  tags                       = local.tags
}

# The deploying principal seeds and manages secret content (e.g. database-credentials).
resource "azurerm_role_assignment" "deployer_kv_secrets" {
  scope                = azurerm_key_vault.this.id
  role_definition_name = "Key Vault Secrets Officer"
  principal_id         = data.azurerm_client_config.current.object_id
}

resource "azurerm_management_lock" "key_vault" {
  count = var.protect_resources ? 1 : 0

  name       = "${local.identifier}-kv-lock"
  scope      = azurerm_key_vault.this.id
  lock_level = "CanNotDelete"
  notes      = "protect_resources is enabled; remove this lock before destroying."
}
