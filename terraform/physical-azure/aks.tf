resource "azurerm_kubernetes_cluster" "this" {
  name                = "${local.identifier}-aks"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  dns_prefix          = local.identifier
  kubernetes_version  = var.aks_kubernetes_version

  sku_tier                  = "Standard"
  automatic_upgrade_channel = "patch"
  node_os_upgrade_channel   = "NodeImage"

  oidc_issuer_enabled       = true
  workload_identity_enabled = true

  # Entra-integrated RBAC is opt-in: requires a customer-supplied admin group.
  # Without it, operators use local accounts via az aks get-credentials.
  local_account_disabled = length(var.aks_admin_group_object_ids) > 0

  dynamic "azure_active_directory_role_based_access_control" {
    for_each = length(var.aks_admin_group_object_ids) > 0 ? [1] : []

    content {
      azure_rbac_enabled     = true
      admin_group_object_ids = var.aks_admin_group_object_ids
    }
  }

  dynamic "api_server_access_profile" {
    for_each = length(var.aks_api_allowed_cidrs) > 0 ? [1] : []

    content {
      # Azure stores bare IPs as /32; normalize so a bare IP from the bootstrap
      # egress-IP allowlist doesn't show a perpetual in-place diff every apply.
      authorized_ip_ranges = [
        for c in var.aks_api_allowed_cidrs : can(regex("/[0-9]+$", c)) ? c : "${c}/32"
      ]
    }
  }

  # Single shared pool for v1: no tainted system pool; "system" is just a name.
  default_node_pool {
    name                 = "system"
    vm_size              = var.aks_node_vm_size
    auto_scaling_enabled = true
    min_count            = var.aks_node_count_min
    max_count            = var.aks_node_count_max
    vnet_subnet_id       = azurerm_subnet.aks.id
    tags                 = local.tags

    upgrade_settings {
      max_surge = "10%"
    }
  }

  identity {
    type = "SystemAssigned"
  }

  network_profile {
    network_plugin      = "azure"
    network_plugin_mode = "overlay"
    network_policy      = "azure"
    pod_cidr            = "192.168.0.0/16"
    service_cidr        = "172.16.0.0/16"
    dns_service_ip      = "172.16.0.10"
    load_balancer_sku   = "standard"
    outbound_type       = "loadBalancer"
  }

  oms_agent {
    log_analytics_workspace_id = azurerm_log_analytics_workspace.this.id
  }

  # Bound auto-upgrade disruption to a window (single pool hosts everything).
  maintenance_window_auto_upgrade {
    frequency   = "Weekly"
    interval    = 1
    duration    = 4
    day_of_week = "Sunday"
    start_time  = "03:00"
    utc_offset  = "+00:00"
  }

  maintenance_window_node_os {
    frequency   = "Weekly"
    interval    = 1
    duration    = 4
    day_of_week = "Sunday"
    start_time  = "03:00"
    utc_offset  = "+00:00"
  }

  tags = local.tags

  lifecycle {
    ignore_changes = [kubernetes_version]
  }
}

# Image pulls come from the customer's ACR (synced from GHCR at deploy time).
resource "azurerm_role_assignment" "aks_acr_pull" {
  count = var.acr_id != "" ? 1 : 0

  scope                = var.acr_id
  role_definition_name = "AcrPull"
  principal_id         = azurerm_kubernetes_cluster.this.kubelet_identity[0].object_id
}
