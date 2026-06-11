variable "subscription_id" {
  description = "Azure subscription ID to deploy into."
  type        = string
}

variable "azure_environment" {
  description = "Azure cloud environment: public or usgovernment."
  type        = string
  default     = "public"

  validation {
    condition     = contains(["public", "usgovernment"], var.azure_environment)
    error_message = "azure_environment must be public or usgovernment."
  }
}

variable "location" {
  description = "Azure region (e.g. eastus2, usgovvirginia)."
  type        = string
}

variable "customer" {
  description = "Customer name, used in resource naming and tagging."
  type        = string
  default     = ""

  validation {
    condition     = var.customer == "" || can(regex("^[a-z0-9][a-z0-9-]{0,8}[a-z0-9]$", var.customer))
    error_message = "customer must be 1-10 characters, lowercase alphanumeric or hyphens, starting and ending with an alphanumeric character."
  }
}

variable "environment" {
  description = "Environment of the application."
  type        = string
  default     = "dev"

  validation {
    condition     = length(var.environment) <= 5
    error_message = "environment must be 5 characters or fewer."
  }
}

variable "protect_resources" {
  description = "Enables deletion protection: management locks on the database and Key Vault purge protection."
  type        = bool
  default     = true
}

variable "external_fqdn" {
  description = "FQDN the application is served on. DNS is customer-managed on Azure; this value is passed through to the logical layer."
  type        = string
}

variable "vnet_cidr" {
  description = "Address space for the VNet."
  type        = string
  default     = "10.10.0.0/16"
}

variable "acr_id" {
  description = "Resource ID of the customer's Azure Container Registry. When set, the AKS kubelet identity is granted AcrPull on it."
  type        = string
  default     = ""
}

variable "alarm_email" {
  description = "Email address for metric alert notifications. Empty disables alerting."
  type        = string
  default     = ""
}

variable "aks_kubernetes_version" {
  description = "Kubernetes version for AKS. Null tracks the AKS default."
  type        = string
  default     = null
}

variable "aks_node_vm_size" {
  description = "VM size for the AKS node pool."
  type        = string
  default     = "Standard_D4s_v5"
}

variable "aks_node_count_min" {
  description = "Minimum node count for the autoscaling node pool."
  type        = number
  default     = 2
}

variable "aks_node_count_max" {
  description = "Maximum node count for the autoscaling node pool."
  type        = number
  default     = 6
}

variable "mysql_sku_name" {
  description = "Azure Database for MySQL Flexible Server SKU."
  type        = string
  default     = "GP_Standard_D2ds_v4"
}

variable "mysql_version" {
  description = "MySQL engine version."
  type        = string
  default     = "8.0.21"
}

variable "mysql_storage_gb" {
  description = "Storage size in GB for MySQL."
  type        = number
  default     = 100
}

variable "mysql_high_availability" {
  description = "Enable zone-redundant high availability for MySQL."
  type        = bool
  default     = true
}

variable "kv_allowed_cidrs" {
  description = "Public egress IPs/CIDRs of the deployment workstation or VM, allowed through the Key Vault firewall to seed secrets. Use bare IPs for single addresses (Azure rejects /32). Required at apply time."
  type        = list(string)
  default     = []
}

variable "mysql_zone" {
  description = "Availability zone for the MySQL primary. Null lets Azure choose; required null in regions without availability zones."
  type        = string
  default     = null
}

variable "mysql_standby_zone" {
  description = "Availability zone for the MySQL HA standby. Null lets Azure choose. Only used when mysql_high_availability is true."
  type        = string
  default     = null
}

variable "mysql_backup_retention_days" {
  description = "Backup retention in days for MySQL."
  type        = number
  default     = 14

  validation {
    condition     = var.mysql_backup_retention_days >= 1 && var.mysql_backup_retention_days <= 35
    error_message = "mysql_backup_retention_days must be between 1 and 35."
  }
}

variable "mysql_geo_redundant_backup" {
  description = "Geo-redundant backups for MySQL. Immutable after server creation: changing it forces a destroy/recreate of the database server."
  type        = bool
  default     = true
}
