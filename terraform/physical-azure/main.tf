terraform {
  required_version = ">= 1.5.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
}

provider "azurerm" {
  subscription_id = var.subscription_id
  environment     = var.azure_environment

  features {
    key_vault {
      purge_soft_delete_on_destroy = !var.protect_resources
    }
  }
}

data "azurerm_client_config" "current" {}

locals {
  identifier         = var.customer != "" ? "${var.customer}-${var.environment}" : "dozuki-${var.environment}"
  identifier_compact = replace(local.identifier, "-", "")

  tags = {
    Terraform   = "true"
    Project     = "cloudprem"
    Identifier  = local.identifier
    Environment = var.environment
  }

  # Private DNS zone suffixes differ between Azure public and Azure Government.
  mysql_private_dns_zone = var.azure_environment == "usgovernment" ? "privatelink.mysql.database.usgovcloudapi.net" : "privatelink.mysql.database.azure.com"
}

# Disambiguates globally-unique names (Key Vault, MySQL server FQDN).
resource "random_string" "suffix" {
  length  = 4
  lower   = true
  numeric = true
  upper   = false
  special = false
}

resource "azurerm_resource_group" "this" {
  name     = "${local.identifier}-cloudprem"
  location = var.location
  tags     = local.tags
}

resource "azurerm_log_analytics_workspace" "this" {
  name                = "${local.identifier}-logs"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  sku                 = "PerGB2018"
  retention_in_days   = 30
  tags                = local.tags
}
