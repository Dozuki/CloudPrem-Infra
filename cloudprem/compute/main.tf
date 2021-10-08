terraform {
  required_providers {
    aws = "3.56.0"
  }
}

provider "kubernetes" {
  host                   = data.aws_eks_cluster.main.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority[0].data)
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

locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-dozuki-${var.environment}"

  cluster_access_role_name = "${local.identifier}-${data.aws_region.current.name}-cluster-access"

  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Identifier  = var.identifier
    Environment = var.environment
  }
}
data "aws_eks_cluster" "main" {
  name = module.eks_cluster.cluster_id
}
data "aws_eks_cluster_auth" "main" {
  name = module.eks_cluster.cluster_id
}

data "aws_partition" "current" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
data "aws_subnets" "private" {
  filter {
    name   = "vpc-id"
    values = [var.vpc_id]
  }

  tags = {
    type = "private"
  }
}
data "aws_subnets" "public" {
  filter {
    name   = "vpc-id"
    values = [var.vpc_id]
  }

  tags = {
    type = "public"
  }
}
data "aws_kms_key" "s3" {
  key_id = var.kms_key_id
}
data "aws_vpc" "main" {
  id = var.vpc_id
}