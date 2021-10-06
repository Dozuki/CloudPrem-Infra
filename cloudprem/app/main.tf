terraform {
  required_providers {
    aws        = "3.56.0"
    kubernetes = "2.4.1"
    helm       = "2.3.0"
    null       = "3.1.0"
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
      api_version = "client.authentication.k8s.io/v1alpha1"
      args        = ["eks", "get-token", "--cluster-name", data.aws_eks_cluster.main.name, "--region", data.aws_region.current.name]
      command     = "aws"
    }
  }
}

#  ############# Locals ############

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

  db_master_host     = jsondecode(data.aws_secretsmanager_secret_version.db_master.secret_string)["host"]
  db_master_username = jsondecode(data.aws_secretsmanager_secret_version.db_master.secret_string)["username"]
  db_master_password = jsondecode(data.aws_secretsmanager_secret_version.db_master.secret_string)["password"]

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


resource "helm_release" "container_insights" {
  name  = "container-insights"
  chart = "${path.module}/charts/container_insights"

  namespace = "default"

  set {
    name  = "cluster_name"
    value = var.eks_cluster_id
  }

  set {
    name  = "region_name"
    value = data.aws_region.current.name
  }
}
