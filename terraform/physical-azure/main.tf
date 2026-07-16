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
    time = {
      source  = "hashicorp/time"
      version = "~> 0.13"
    }
  }
}

provider "azurerm" {
  subscription_id = var.subscription_id
  tenant_id       = var.tenant_id == "" ? null : var.tenant_id
  environment     = var.azure_environment

  features {
    key_vault {
      purge_soft_delete_on_destroy = !var.protect_resources
    }
    # AKS's monitoring add-on auto-creates a ContainerInsights solution in the
    # RG that isn't tracked in state; without this, destroying the RG fails
    # because it "still contains resources". Let the provider clear it.
    resource_group {
      prevent_deletion_if_contains_resources = false
    }
  }
}

data "azurerm_client_config" "current" {}

locals {
  identifier         = var.customer != "" ? "${var.customer}-${var.environment}" : "dozuki-${var.environment}"
  identifier_compact = replace(local.identifier, "-", "")

  tags = {
    Terraform   = "true"
    Project     = "mpc"
    Service     = "mpc"
    Customer    = coalesce(var.customer, "dozuki")
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
  name     = "${local.identifier}-mpc"
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
