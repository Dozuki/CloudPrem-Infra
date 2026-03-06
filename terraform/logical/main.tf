terraform {
  required_providers {
    aws        = { source = "hashicorp/aws" }
    kubernetes = { source = "hashicorp/kubernetes" }
    helm       = { source = "hashicorp/helm" }
    vault      = { source = "hashicorp/vault" }
    null       = { source = "hashicorp/null" }
    local      = { source = "hashicorp/local" }
    random     = { source = "hashicorp/random" }
  }
}

provider "kubernetes" {
  host                   = data.aws_eks_cluster.main.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority[0].data)
  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    args        = ["eks", "get-token", "--cluster-name", data.aws_eks_cluster.main.name, "--region", data.aws_region.current.id, "--profile", var.aws_profile]
    command     = "aws"
  }
}

provider "helm" {
  kubernetes = {
    host                   = data.aws_eks_cluster.main.endpoint
    cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority[0].data)
    exec = {
      api_version = "client.authentication.k8s.io/v1beta1"
      args        = ["eks", "get-token", "--cluster-name", data.aws_eks_cluster.main.name, "--region", data.aws_region.current.id, "--profile", var.aws_profile]
      command     = "aws"
    }
  }
}

provider "vault" {
  # Address set via VAULT_ADDR env var. The operator port-forwards to the
  # Vault cluster (localhost:8200) when applying from a workstation.
  # var.vault_address is the in-cluster PrivateLink address used by apps.
  skip_child_token = true
}

locals {
  is_us_gov        = data.aws_partition.current.partition == "aws-us-gov"
  ca_cert_pem_file = local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/global-bundle.pem"

  # Database
  db_credentials = jsondecode(data.aws_secretsmanager_secret_version.db_master.secret_string)

  db_master_host     = local.db_credentials["host"]
  db_master_username = local.db_credentials["username"]
  db_master_password = local.db_credentials["password"]

  db_bi_host     = var.enable_bi ? jsondecode(data.aws_secretsmanager_secret_version.db_bi[0].secret_string)["host"] : ""
  db_bi_password = var.enable_bi ? jsondecode(data.aws_secretsmanager_secret_version.db_bi[0].secret_string)["password"] : ""

  # Grafana
  grafana_url            = var.enable_bi ? format("https://%s/%s", var.dns_domain_name, var.grafana_subpath) : null
  grafana_admin_username = var.enable_bi ? var.customer != "" ? var.customer : "dozuki" : "dozuki"
  grafana_admin_password = var.enable_bi ? nonsensitive(random_password.grafana_admin[0].result) : ""

  # Kubernetes
  k8s_namespace_name = "dozuki"

  // Map for app config

  secret_values            = var.enable_vault ? {} : jsondecode(data.aws_secretsmanager_secret_version.devops_secret_version[0].secret_string)
  secret_values_structured = { for key, value in local.secret_values : key => { value = value } }

  base_config_values = {
    customer               = { value = coalesce(var.customer, "Dozuki") }
    environment            = { value = var.environment }
    aws_acct_id            = { value = data.aws_caller_identity.current.account_id }
    aws_region             = { value = data.aws_region.current.id }
    hostname               = { value = var.dns_domain_name }
    bi_enabled             = { value = var.enable_bi ? "true" : "false" }
    webhooks_enabled       = { value = var.enable_webhooks ? "true" : "false" }
    memcached_host         = { value = var.memcached_cluster_address }
    s3_kms_key             = { value = data.aws_kms_key.s3.arn }
    s3_images_bucket       = { value = var.s3_images_bucket }
    s3_objects_bucket      = { value = var.s3_objects_bucket }
    s3_documents_bucket    = { value = var.s3_documents_bucket }
    s3_pdfs_bucket         = { value = var.s3_pdfs_bucket }
    db_host                = { value = local.db_master_host }
    db_user                = { value = local.db_master_username }
    db_password            = { value = local.db_master_password }
    rds_ca_cert            = { value = base64encode(file(local.ca_cert_pem_file)) }
    msk_bootstrap_brokers  = { value = var.msk_bootstrap_brokers }
    google_translate_token = { value = var.google_translate_api_token }
    dns_validation         = { value = !local.is_us_gov && contains(["dozuki.cloud", "dozuki.com", "dozuki.app", "dozuki.guide"], replace(var.dns_domain_name, "/^[^.]+\\./", "")) ? "true" : "false" }
    vault_enabled          = { value = var.enable_vault ? "true" : "false" }
    vault_address          = { value = var.vault_address }
    image_repository       = { value = var.image_repository }
    image_tag              = { value = var.image_tag }
    nextjs_tag             = { value = var.nextjs_tag }
    smtp_enabled           = { value = var.smtp_enabled ? "true" : "false" }
    smtp_host              = { value = var.smtp_host }
    smtp_from_address      = { value = var.smtp_from_address }
    smtp_auth_enabled      = { value = var.smtp_auth_enabled ? "true" : "false" }
    smtp_username          = { value = var.smtp_username }
    smtp_password          = { value = var.smtp_password }
  }

  // Optional add-on for Grafana config
  grafana_config_values = {
    grafana_admin_username      = { value = local.grafana_admin_username }
    grafana_admin_password      = { value = local.grafana_admin_password }
    grafana_datasource_hostname = { value = local.db_bi_host }
    grafana_datasource_password = { value = local.db_bi_password }
    grafana_settings_hostname   = { value = local.db_master_host }
    grafana_settings_username   = { value = local.db_master_username }
    grafana_settings_password   = { value = local.db_master_password }
    grafana_subpath             = { value = try(var.grafana_subpath, "") }
  }

  all_config_values      = merge(local.base_config_values, local.grafana_config_values, local.secret_values_structured, local.vault_config_values)
  all_config_values_flat = { for key, value in local.all_config_values : key => value.value }

}

data "aws_secretsmanager_secret" "devops_secret" {
  count = var.enable_vault ? 0 : 1
  name  = var.devops_secret_name
}

data "aws_secretsmanager_secret_version" "devops_secret_version" {
  count     = var.enable_vault ? 0 : 1
  secret_id = data.aws_secretsmanager_secret.devops_secret[0].id
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