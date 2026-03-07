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

  secret_values = jsondecode(data.aws_secretsmanager_secret_version.devops_secret_version.secret_string)

  sensitive_helm_values = {
    db_password               = local.db_master_password
    smtp_password             = try(local.secret_values["smtp_password"], "")
    sentry_dsn                = try(local.secret_values["sentry_dsn"], "")
    frontegg_client_id        = try(local.secret_values["frontegg_client_id"], "")
    frontegg_api_token        = try(local.secret_values["frontegg_api_token"], "")
    frontegg_docker_username  = try(local.secret_values["frontegg_docker_username"], "")
    frontegg_docker_password  = try(local.secret_values["frontegg_docker_password"], "")
    frontegg_auth_pubkey      = try(local.secret_values["frontegg_auth_pubkey"], "")
    surveyjs_license_key      = try(local.secret_values["surveyjs_license_key"], "")
    google_translate_token    = var.google_translate_api_token
    rustici_password          = try(local.secret_values["rustici_password"], "")
    rustici_managed_password  = try(local.secret_values["rustici_managed_password"], "")
    ops_basic_auth            = try(local.secret_values["ops_basic_auth"], "")
    infra_auth_password       = try(local.secret_values["infra_auth_password"], "")
    grafana_smtp_enabled      = try(local.secret_values["grafana_smtp_enabled"], "false")
    grafana_smtp_host         = try(local.secret_values["grafana_smtp_host"], "")
    grafana_smtp_user         = try(local.secret_values["grafana_smtp_user"], "")
    grafana_smtp_password     = try(local.secret_values["grafana_smtp_password"], "")
    grafana_smtp_from_address = try(local.secret_values["grafana_smtp_from_address"], "")
    grafana_smtp_starttls     = try(local.secret_values["grafana_smtp_starttls"], "OpportunisticStartTLS")
  }

  dns_validation = !local.is_us_gov && contains(["dozuki.cloud", "dozuki.com", "dozuki.app", "dozuki.guide"], replace(var.dns_domain_name, "/^[^.]+\\./", ""))

}

data "aws_secretsmanager_secret" "devops_secret" {
  name = var.devops_secret_name
}

data "aws_secretsmanager_secret_version" "devops_secret_version" {
  secret_id = data.aws_secretsmanager_secret.devops_secret.id
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