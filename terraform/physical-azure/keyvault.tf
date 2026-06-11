# Key Vault names: 3-24 chars, globally unique.
# "kv-" + identifier_compact (max 15) + "-" + 4-char suffix = max 23.
resource "azurerm_key_vault" "this" {
  name                       = "kv-${local.identifier_compact}-${random_string.suffix.result}"
  location                   = azurerm_resource_group.this.location
  resource_group_name        = azurerm_resource_group.this.name
  tenant_id                  = data.azurerm_client_config.current.tenant_id
  sku_name                   = "standard"
  rbac_authorization_enabled = true
  purge_protection_enabled   = var.protect_resources
  soft_delete_retention_days = 7
  tags                       = local.tags
}

# The deploying principal manages secret content (e.g. seeding database-credentials).
resource "azurerm_role_assignment" "deployer_kv_admin" {
  scope                = azurerm_key_vault.this.id
  role_definition_name = "Key Vault Administrator"
  principal_id         = data.azurerm_client_config.current.object_id
}

resource "azurerm_management_lock" "key_vault" {
  count = var.protect_resources ? 1 : 0

  name       = "${local.identifier}-kv-lock"
  scope      = azurerm_key_vault.this.id
  lock_level = "CanNotDelete"
  notes      = "protect_resources is enabled; remove this lock before destroying."
}
