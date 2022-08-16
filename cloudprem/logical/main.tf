terraform {
  required_providers {
    aws        = "4.25.0"
    kubernetes = "2.12.1"
    helm       = "2.6.0"
    null       = "3.1.1"
  }
}

provider "kubernetes" {
  host                   = data.aws_eks_cluster.main.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority.0.data)
  token                  = data.aws_eks_cluster_auth.main.token
}

provider "helm" {
  kubernetes {
    host                   = data.aws_eks_cluster.main.endpoint
    cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority.0.data)
    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      args        = ["eks", "get-token", "--cluster-name", data.aws_eks_cluster.main.name, "--region", data.aws_region.current.name, "--profile", var.aws_profile]
      command     = "aws"
    }
  }
}

locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-dozuki-${var.environment}"

  dozuki_license_parameter_name = var.dozuki_license_parameter_name == "" ? (var.identifier == "" ? "/dozuki/${var.environment}/license" : "/${var.identifier}/dozuki/${var.environment}/license") : var.dozuki_license_parameter_name

  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Identifier  = var.identifier
    Environment = var.environment
  }

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  ca_cert_pem_file = local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/rds-ca-2019-root.pem"

  db_master_host     = jsondecode(data.aws_secretsmanager_secret_version.db_master.secret_string)["host"]
  db_master_username = jsondecode(data.aws_secretsmanager_secret_version.db_master.secret_string)["username"]
  db_master_password = jsondecode(data.aws_secretsmanager_secret_version.db_master.secret_string)["password"]

  frontegg_clientid = try(data.kubernetes_secret.frontegg[0].data.clientid, "")
  frontegg_apikey   = try(data.kubernetes_secret.frontegg[0].data.apikey, "")
  frontegg_pub_key  = try(data.kubernetes_secret.frontegg[0].data.pubkey, "")
  frontegg_username = try(data.kubernetes_secret.frontegg[0].data.username, "")
  frontegg_password = try(data.kubernetes_secret.frontegg[0].data.password, "") #tfsec:ignore:general-secrets-no-plaintext-exposure

}

data "aws_eks_cluster" "main" {
  name = var.eks_cluster_id
}

data "aws_eks_cluster_auth" "main" {
  name = var.eks_cluster_id
}

data "aws_partition" "current" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

data "aws_kms_key" "s3" {
  key_id = var.s3_kms_key_id
}

data "aws_vpc" "main" {
  id = var.vpc_id
}

data "aws_subnets" "private" {
  filter {
    name   = "vpc-id"
    values = [var.vpc_id]
  }

  tags = {
    type = "private"
  }
}

data "aws_secretsmanager_secret_version" "db_master" {
  secret_id = var.primary_db_secret
}