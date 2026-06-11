resource "random_password" "database" {
  length  = 32
  special = false
}

resource "azurerm_mysql_flexible_server" "this" {
  name                = "${local.identifier}-mysql-${random_string.suffix.result}"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name

  administrator_login    = "dozuki"
  administrator_password = random_password.database.result

  version  = var.mysql_version
  sku_name = var.mysql_sku_name
  zone     = "1"

  delegated_subnet_id = azurerm_subnet.mysql.id
  private_dns_zone_id = azurerm_private_dns_zone.mysql.id

  backup_retention_days        = 14
  geo_redundant_backup_enabled = var.protect_resources

  dynamic "high_availability" {
    for_each = var.mysql_high_availability ? [1] : []

    content {
      mode                      = "ZoneRedundant"
      standby_availability_zone = "2"
    }
  }

  storage {
    size_gb           = var.mysql_storage_gb
    auto_grow_enabled = true
  }

  tags = local.tags

  depends_on = [azurerm_private_dns_zone_virtual_network_link.mysql]
}

# Azure defaults this ON; the app connects without TLS today (parity with RDS).
resource "azurerm_mysql_flexible_server_configuration" "require_secure_transport" {
  name                = "require_secure_transport"
  resource_group_name = azurerm_resource_group.this.name
  server_name         = azurerm_mysql_flexible_server.this.name
  value               = "OFF"
}

resource "azurerm_management_lock" "mysql" {
  count = var.protect_resources ? 1 : 0

  name       = "${local.identifier}-mysql-lock"
  scope      = azurerm_mysql_flexible_server.this.id
  lock_level = "CanNotDelete"
  notes      = "protect_resources is enabled; remove this lock before destroying."
}

# Same JSON shape as the AWS Secrets Manager secret (see terraform/CONTRACT.md).
resource "azurerm_key_vault_secret" "database_credentials" {
  name         = "database-credentials"
  key_vault_id = azurerm_key_vault.this.id
  content_type = "application/json"

  value = jsonencode({
    host     = azurerm_mysql_flexible_server.this.fqdn
    port     = 3306
    username = azurerm_mysql_flexible_server.this.administrator_login
    password = random_password.database.result
  })

  depends_on = [time_sleep.kv_rbac_propagation]
}
