terraform {
  required_providers {
    aws        = "4.25.0"
    kubernetes = "2.12.1"
    random     = "3.3.2"
  }
}
provider "kubernetes" {
  host                   = data.aws_eks_cluster.main.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority.0.data)
  token                  = data.aws_eks_cluster_auth.main.token
}

locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-dozuki-${var.environment}"

  # EKS
  cluster_access_role_name = "${local.identifier}-${data.aws_region.current.name}-cluster-access"
  create_eks_kms           = var.eks_kms_key_id == "" ? true : false
  eks_kms_key              = local.create_eks_kms ? aws_kms_key.eks[0].arn : data.aws_kms_key.eks[0].arn

  # Tags for all resources. If you add a tag, it must never be blank.
  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Identifier  = coalesce(var.identifier, "-")
    Environment = var.environment
  }

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  # Database
  rds_parameter_group_name = var.enable_bi ? aws_db_parameter_group.bi[0].id : aws_db_parameter_group.default.id
  ca_cert_identifier       = local.is_us_gov ? "rds-ca-rsa4096-g1" : "rds-ca-2019"
  ca_cert_pem_file         = local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/rds-ca-2019-root.pem"
  bi_subnet_ids            = var.bi_public_access ? local.public_subnet_ids : local.private_subnet_ids
  bi_access_cidrs          = length(var.bi_access_cidrs) == 0 ? [var.vpc_cidr] : var.bi_access_cidrs

  # S3 Buckets
  guide_buckets  = var.create_s3_buckets ? aws_s3_bucket.guide_buckets : data.aws_s3_bucket.guide_buckets
  logging_bucket = var.create_s3_buckets ? aws_s3_bucket.logging_bucket[0].bucket : data.aws_s3_bucket.logging[0].bucket

  create_s3_bucket_names   = var.create_s3_buckets ? ["dozuki-guide-images", "dozuki-guide-objects", "dozuki-guide-pdfs", "dozuki-guide-documents"] : []
  existing_s3_bucket_names = !var.create_s3_buckets ? var.s3_bucket_names : []

  # VPC
  azs_count          = var.azs_count
  create_vpc         = var.vpc_id == "" ? true : false
  vpc_id             = local.create_vpc ? module.vpc[0].vpc_id : var.vpc_id
  vpc_cidr           = local.create_vpc ? module.vpc[0].vpc_cidr_block : data.aws_vpc.this[0].cidr_block
  public_subnet_ids  = local.create_vpc ? module.vpc[0].public_subnets : data.aws_subnets.public[0].ids
  private_subnet_ids = local.create_vpc ? module.vpc[0].private_subnets : data.aws_subnets.private[0].ids
}
data "aws_eks_cluster" "main" {
  name = module.eks_cluster.cluster_id
}
data "aws_eks_cluster_auth" "main" {
  name = module.eks_cluster.cluster_id
}

data "aws_partition" "current" {}
data "aws_availability_zones" "available" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

data "aws_vpc" "this" {
  count = local.create_vpc ? 0 : 1
  id    = var.vpc_id
}
data "aws_subnets" "public" {
  count = local.create_vpc ? 0 : 1

  filter {
    name   = "vpc-id"
    values = [var.vpc_id]
  }

  tags = {
    type = "public"
  }
}
data "aws_subnets" "private" {
  count = local.create_vpc ? 0 : 1

  filter {
    name   = "vpc-id"
    values = [var.vpc_id]
  }

  tags = {
    type = "private"
  }
}

data "aws_kms_key" "rds" {
  key_id = var.rds_kms_key_id
}
data "aws_kms_key" "s3" {
  key_id = var.s3_kms_key_id
}
data "aws_kms_key" "eks" {
  count = local.create_eks_kms ? 0 : 1

  key_id = var.eks_kms_key_id
}