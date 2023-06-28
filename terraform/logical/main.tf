terraform {
  required_version = ">= 1.3.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 4.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.0"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
}

provider "kubernetes" {
  host                   = data.aws_eks_cluster.main.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority[0].data)
  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    args        = ["eks", "get-token", "--cluster-name", data.aws_eks_cluster.main.name, "--region", data.aws_region.current.name, "--profile", var.aws_profile]
    command     = "aws"
  }
}

provider "helm" {
  kubernetes {
    host                   = data.aws_eks_cluster.main.endpoint
    cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority[0].data)
    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      args        = ["eks", "get-token", "--cluster-name", data.aws_eks_cluster.main.name, "--region", data.aws_region.current.name, "--profile", var.aws_profile]
      command     = "aws"
    }
  }
}

locals {
  dozuki_customer_id_parameter_name = var.dozuki_customer_id_parameter_name == "" ? (var.customer == "" ? "/dozuki/${var.environment}/customer_id" : "/${var.customer}/dozuki/${var.environment}/customer_id") : var.dozuki_customer_id_parameter_name

  is_us_gov        = data.aws_partition.current.partition == "aws-us-gov"
  ca_cert_pem_file = local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/rds-ca-2019-root.pem"

  aws_profile_prefix = var.aws_profile != "" ? "AWS_PROFILE=${var.aws_profile}" : ""

  # Database
  db_credentials = jsondecode(data.aws_secretsmanager_secret_version.db_master.secret_string)

  db_master_host     = local.db_credentials["host"]
  db_master_username = local.db_credentials["username"]
  db_master_password = local.db_credentials["password"]

  db_bi_host     = var.enable_bi ? jsondecode(data.aws_secretsmanager_secret_version.db_bi[0].secret_string)["host"] : ""
  db_bi_password = var.enable_bi ? jsondecode(data.aws_secretsmanager_secret_version.db_bi[0].secret_string)["password"] : ""

  # Webhooks
  frontegg_clientid = try(data.kubernetes_secret.frontegg[0].data.clientid, "")
  frontegg_apikey   = try(data.kubernetes_secret.frontegg[0].data.apikey, "")
  frontegg_pub_key  = try(data.kubernetes_secret.frontegg[0].data.pubkey, "")
  frontegg_username = try(data.kubernetes_secret.frontegg[0].data.username, "")
  frontegg_password = try(data.kubernetes_secret.frontegg[0].data.password, "") #tfsec:ignore:general-secrets-no-plaintext-exposure

  # Grafana
  grafana_url            = var.enable_bi ? format("https://%s/dashboards", var.dns_domain_name) : null
  grafana_admin_username = var.enable_bi ? var.customer != "" ? var.customer : "dozuki" : null
  grafana_admin_password = var.enable_bi ? nonsensitive(random_password.grafana_admin[0].result) : null

  # Replicated
  app_slug           = "dozukikots"
  k8s_namespace_name = "dozuki"
  app_and_channel    = "${local.app_slug}${var.replicated_channel != "" ? "/" : ""}${var.replicated_channel}"

  base_config_values = {
    hostname   = { value = var.dns_domain_name }
    bi_enabled = { value = var.enable_bi ? "1" : "0" }
  }

  grafana_config_values = var.enable_bi ? {
    grafana_admin_username      = { value = local.grafana_admin_username }
    grafana_admin_password      = { value = random_password.grafana_admin[0].result }
    grafana_datasource_hostname = { value = local.db_bi_host }
    grafana_datasource_password = { value = local.db_bi_password }
    grafana_settings_hostname   = { value = local.db_master_host }
    grafana_settings_username   = { value = local.db_master_username }
    grafana_settings_password   = { value = local.db_master_password }
  } : {}

  #  app_feature_values = { for feature in var.feature_flags_enabled : feature => 1 }

  all_config_values = merge(local.base_config_values, local.grafana_config_values)



}

data "aws_eks_cluster" "main" {
  name = var.eks_cluster_id
}

data "aws_partition" "current" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

data "aws_kms_key" "s3" {
  key_id = var.s3_kms_key_id
}

data "aws_secretsmanager_secret_version" "db_master" {
  secret_id = var.primary_db_secret
}
data "aws_secretsmanager_secret_version" "db_bi" {
  count     = var.enable_bi ? 1 : 0
  secret_id = var.bi_database_credential_secret
}