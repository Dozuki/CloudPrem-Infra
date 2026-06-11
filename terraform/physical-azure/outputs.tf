output "cluster_name" {
  description = "AKS cluster name."
  value       = azurerm_kubernetes_cluster.this.name
}

output "cluster_oidc_issuer_url" {
  description = "OIDC issuer URL for workload-identity federation."
  value       = azurerm_kubernetes_cluster.this.oidc_issuer_url
}

output "node_resource_group" {
  description = "AKS-managed resource group (cloud controller places LBs and disks here)."
  value       = azurerm_kubernetes_cluster.this.node_resource_group
}

output "resource_group_name" {
  description = "Resource group containing all CloudPrem resources."
  value       = azurerm_resource_group.this.name
}

output "location" {
  description = "Azure region."
  value       = azurerm_resource_group.this.location
}

output "tenant_id" {
  description = "Entra tenant ID."
  value       = data.azurerm_client_config.current.tenant_id
}

output "vnet_id" {
  description = "VNet resource ID."
  value       = azurerm_virtual_network.this.id
}

output "aks_subnet_id" {
  description = "Subnet hosting AKS nodes."
  value       = azurerm_subnet.aks.id
}

output "dns_domain_name" {
  description = "FQDN the application is served on (customer-managed DNS)."
  value       = var.external_fqdn
}

output "db_host" {
  description = "MySQL Flexible Server FQDN."
  value       = azurerm_mysql_flexible_server.this.fqdn
}

output "db_credentials_secret_id" {
  description = "Key Vault secret (versionless ID) holding database credentials JSON."
  value       = azurerm_key_vault_secret.database_credentials.versionless_id
}

output "key_vault_uri" {
  description = "Key Vault URI for the ESO ClusterSecretStore."
  value       = azurerm_key_vault.this.vault_uri
}

output "key_vault_id" {
  description = "Key Vault resource ID."
  value       = azurerm_key_vault.this.id
}

output "eso_identity_client_id" {
  description = "Client ID of the external-secrets workload identity."
  value       = azurerm_user_assigned_identity.eso.client_id
}

output "guide_images_bucket" {
  description = "SeaweedFS bucket name for guide images (created in-cluster by the logical layer)."
  value       = "${local.identifier}-guide-images"
}

output "guide_objects_bucket" {
  description = "SeaweedFS bucket name for guide objects."
  value       = "${local.identifier}-guide-objects"
}

output "guide_pdfs_bucket" {
  description = "SeaweedFS bucket name for guide PDFs."
  value       = "${local.identifier}-guide-pdfs"
}

output "documents_bucket" {
  description = "SeaweedFS bucket name for documents."
  value       = "${local.identifier}-documents"
}
