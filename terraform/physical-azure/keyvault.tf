# Key Vault names: 3-24 chars, globally unique.
# "kv-" + identifier_compact (max 15) + "-" + 4-char suffix = max 23.
# Firewall mirrors the AKS API (aks.tf): with kv_allowed_cidrs set, deny by
# default and allowlist those CIDRs (the kit pins the operator's egress IP);
# with it empty, the vault is public (default_action = Allow), RBAC-gated only —
# required on the Spacelift public-worker path, where worker egress IPs aren't
# allowlistable. In-cluster access (ESO) always rides the AKS subnet's Key Vault
# service endpoint regardless.
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

  network_acls {
    default_action             = length(var.kv_allowed_cidrs) > 0 ? "Deny" : "Allow"
    bypass                     = "AzureServices"
    virtual_network_subnet_ids = [azurerm_subnet.aks.id]
    ip_rules                   = var.kv_allowed_cidrs
  }

  tags = local.tags
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

# Azure RBAC grants take time to propagate to the Key Vault data plane; without
# this, the first secret write after vault creation often fails with 403.
resource "time_sleep" "kv_rbac_propagation" {
  create_duration = "60s"

  depends_on = [azurerm_role_assignment.deployer_kv_secrets]
}
