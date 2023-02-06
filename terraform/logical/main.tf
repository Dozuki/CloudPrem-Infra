terraform {
  required_version = ">= 1.1.0"

  required_providers {
    aws        = "3.70.0"
    kubernetes = "2.13.1"
    helm       = "2.3.0"
    null       = "3.1.0"
    # This provider needs to stay for awhile to maintain backwards compatibility with older infra versions (<=2.5.4)
    local  = "2.2.3"
    random = "3.4.3"
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
  dozuki_customer_id_parameter_name = var.dozuki_customer_id_parameter_name == "" ? (var.identifier == "" ? "/dozuki/${var.environment}/customer_id" : "/${var.identifier}/dozuki/${var.environment}/customer_id") : var.dozuki_customer_id_parameter_name

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  ca_cert_pem_file = local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/rds-ca-2019-root.pem"

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
  grafana_url             = var.enable_bi ? format("https://%s:3000", var.nlb_dns_name) : null
  grafana_admin_username  = var.enable_bi ? "dozuki" : null
  grafana_admin_password  = var.enable_bi ? nonsensitive(random_password.grafana_admin[0].result) : null
  grafana_ssl_cert_cn     = var.grafana_ssl_cn == "" ? var.nlb_dns_name : var.grafana_ssl_cn
  grafana_ssl_secret_name = var.grafana_use_replicated_ssl ? "www-tls" : kubernetes_secret.grafana_ssl[0].metadata[0].name

  # Replicated
  app_slug        = "dozukikots"
  k8s_namespace   = "dozuki"
  app_and_channel = "${local.app_slug}${var.replicated_channel != "" ? "/" : ""}${var.replicated_channel}"

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