resource "azurerm_monitor_action_group" "alarms" {
  count = var.alarm_email != "" ? 1 : 0

  name                = "${local.identifier}-alarms"
  resource_group_name = azurerm_resource_group.this.name
  short_name          = "cpalarms"

  email_receiver {
    name          = "ops"
    email_address = var.alarm_email
  }

  tags = local.tags
}

resource "azurerm_monitor_metric_alert" "mysql_cpu" {
  count = var.alarm_email != "" ? 1 : 0

  name                = "${local.identifier}-mysql-cpu"
  resource_group_name = azurerm_resource_group.this.name
  scopes              = [azurerm_mysql_flexible_server.this.id]
  description         = "MySQL CPU above 80% for 15 minutes."
  severity            = 2
  frequency           = "PT5M"
  window_size         = "PT15M"

  criteria {
    metric_namespace = "Microsoft.DBforMySQL/flexibleServers"
    metric_name      = "cpu_percent"
    aggregation      = "Average"
    operator         = "GreaterThan"
    threshold        = 80
  }

  action {
    action_group_id = azurerm_monitor_action_group.alarms[0].id
  }

  tags = local.tags
}

resource "azurerm_monitor_metric_alert" "mysql_storage" {
  count = var.alarm_email != "" ? 1 : 0

  name                = "${local.identifier}-mysql-storage"
  resource_group_name = azurerm_resource_group.this.name
  scopes              = [azurerm_mysql_flexible_server.this.id]
  description         = "MySQL storage above 85%."
  severity            = 1
  frequency           = "PT5M"
  window_size         = "PT15M"

  criteria {
    metric_namespace = "Microsoft.DBforMySQL/flexibleServers"
    metric_name      = "storage_percent"
    aggregation      = "Average"
    operator         = "GreaterThan"
    threshold        = 85
  }

  action {
    action_group_id = azurerm_monitor_action_group.alarms[0].id
  }

  tags = local.tags
}

resource "azurerm_monitor_metric_alert" "mysql_memory" {
  count = var.alarm_email != "" ? 1 : 0

  name                = "${local.identifier}-mysql-memory"
  resource_group_name = azurerm_resource_group.this.name
  scopes              = [azurerm_mysql_flexible_server.this.id]
  description         = "MySQL memory above 90% for 15 minutes."
  severity            = 2
  frequency           = "PT5M"
  window_size         = "PT15M"

  criteria {
    metric_namespace = "Microsoft.DBforMySQL/flexibleServers"
    metric_name      = "memory_percent"
    aggregation      = "Average"
    operator         = "GreaterThan"
    threshold        = 90
  }

  action {
    action_group_id = azurerm_monitor_action_group.alarms[0].id
  }

  tags = local.tags
}

resource "azurerm_monitor_metric_alert" "aks_node_cpu" {
  count = var.alarm_email != "" ? 1 : 0

  name                = "${local.identifier}-aks-node-cpu"
  resource_group_name = azurerm_resource_group.this.name
  scopes              = [azurerm_kubernetes_cluster.this.id]
  description         = "AKS node CPU above 90% for 15 minutes."
  severity            = 2
  frequency           = "PT5M"
  window_size         = "PT15M"

  criteria {
    metric_namespace = "Microsoft.ContainerService/managedClusters"
    metric_name      = "node_cpu_usage_percentage"
    aggregation      = "Average"
    operator         = "GreaterThan"
    threshold        = 90
  }

  action {
    action_group_id = azurerm_monitor_action_group.alarms[0].id
  }

  tags = local.tags
}
