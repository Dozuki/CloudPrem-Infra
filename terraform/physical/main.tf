terraform {
  required_version = ">= 1.2.0"

  required_providers {
    aws        = "4.57.0"
    random     = "3.4.3"
    kubernetes = "2.18.1"
    null       = "3.2.1"
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

locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-${var.environment}"

  # EKS
  cluster_access_role_name = "${local.identifier}-${data.aws_region.current.name}-cluster-access"
  create_eks_kms           = var.eks_kms_key_id == "" ? true : false
  eks_kms_key              = local.create_eks_kms ? aws_kms_key.eks[0].arn : data.aws_kms_key.eks[0].arn

  # Tags for all resources. If you add a tag, it must never be blank.
  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Identifier  = coalesce(var.identifier, "NA")
    Environment = var.environment
  }

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  # Database
  rds_parameter_group_name = var.enable_bi ? aws_db_parameter_group.bi[0].id : aws_db_parameter_group.default.id
  ca_cert_identifier       = local.is_us_gov ? "rds-ca-rsa4096-g1" : "rds-ca-2019"
  ca_cert_pem_file         = local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/rds-ca-2019-root.pem"
  bi_subnet_ids            = var.bi_public_access ? local.public_subnet_ids : local.private_subnet_ids
  grafana_ssl_cert_cn      = var.grafana_ssl_cn == "" ? module.nlb.lb_dns_name : var.grafana_ssl_cn

  # Access Config
  secure_default_bi_access_cidrs      = length(var.bi_access_cidrs) == 0 ? [local.vpc_cidr] : var.bi_access_cidrs
  secure_default_grafana_access_cidrs = length(var.grafana_access_cidrs) == 0 ? [local.vpc_cidr] : var.grafana_access_cidrs
  bi_access_cidrs                     = local.secure_default_bi_access_cidrs != tolist(["0.0.0.0/0"]) && local.secure_default_bi_access_cidrs != [local.vpc_cidr] ? concat([local.vpc_cidr], var.bi_access_cidrs) : local.secure_default_bi_access_cidrs
  grafana_access_cidrs                = local.secure_default_grafana_access_cidrs != tolist(["0.0.0.0/0"]) && local.secure_default_grafana_access_cidrs != [local.vpc_cidr] ? concat([local.vpc_cidr], var.grafana_access_cidrs) : local.secure_default_grafana_access_cidrs
  app_access_cidrs                    = var.app_access_cidrs != tolist(["0.0.0.0/0"]) ? concat([local.vpc_cidr], var.app_access_cidrs) : var.app_access_cidrs
  replicated_ui_access_cidrs          = var.replicated_ui_access_cidrs != tolist(["0.0.0.0/0"]) ? concat([local.vpc_cidr], var.replicated_ui_access_cidrs) : var.replicated_ui_access_cidrs

  # S3 Buckets
  guide_images_bucket  = var.create_s3_buckets ? aws_s3_bucket.guide_images[0].bucket : data.aws_s3_bucket.guide_images[0].bucket
  guide_objects_bucket = var.create_s3_buckets ? aws_s3_bucket.guide_objects[0].bucket : data.aws_s3_bucket.guide_objects[0].bucket
  guide_pdfs_bucket    = var.create_s3_buckets ? aws_s3_bucket.guide_pdfs[0].bucket : data.aws_s3_bucket.guide_pdfs[0].bucket
  documents_bucket     = var.create_s3_buckets ? aws_s3_bucket.guide_documents[0].bucket : data.aws_s3_bucket.documents[0].bucket
  logging_bucket       = var.create_s3_buckets ? aws_s3_bucket.logging_bucket[0].bucket : data.aws_s3_bucket.logging[0].bucket

  # VPC
  azs_count          = var.azs_count
  create_vpc         = var.vpc_id == "" ? true : false
  vpc_id             = local.create_vpc ? module.vpc[0].vpc_id : var.vpc_id
  vpc_cidr           = local.create_vpc ? module.vpc[0].vpc_cidr_block : data.aws_vpc.this[0].cidr_block
  public_subnet_ids  = local.create_vpc ? module.vpc[0].public_subnets : data.aws_subnets.public[0].ids
  private_subnet_ids = local.create_vpc ? module.vpc[0].private_subnets : data.aws_subnets.private[0].ids
}

# Provider and global data resources
data "aws_eks_cluster" "main" {
  name = module.eks_cluster.cluster_id
}
data "aws_partition" "current" {}
data "aws_availability_zones" "available" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
