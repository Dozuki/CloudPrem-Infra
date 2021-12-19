terraform {
  required_providers {
    aws    = "3.70.0"
    random = "3.1.0"
  }
}
provider "kubernetes" {
  host                   = data.aws_eks_cluster.main.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority.0.data)
  token                  = data.aws_eks_cluster_auth.main.token
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

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  # Database
  bi_rds_parameters = [
    {
      name  = "binlog_format"
      value = "ROW"
    },
    {
      name  = "binlog_row_image"
      value = "Full"
    },
    {
      name  = "binlog_checksum"
      value = "NONE"
    }
  ]
  # We have to specify a value for the false variation of this conditional due to the RDS modules inability to handle
  # an empty parameter group. So we use a value that is already a default to achieve the same thing.
  rds_parameters     = var.enable_bi ? local.bi_rds_parameters : [{ name = "binlog_format", value = "MIXED" }]
  ca_cert_identifier = local.is_us_gov ? "rds-ca-2017" : "rds-ca-2019"

  # S3 Buckets
  guide_images_bucket  = var.create_s3_buckets ? aws_s3_bucket.guide_images[0].bucket : data.aws_s3_bucket.guide_images[0].bucket
  guide_objects_bucket = var.create_s3_buckets ? aws_s3_bucket.guide_objects[0].bucket : data.aws_s3_bucket.guide_objects[0].bucket
  guide_pdfs_bucket    = var.create_s3_buckets ? aws_s3_bucket.guide_pdfs[0].bucket : data.aws_s3_bucket.guide_pdfs[0].bucket
  documents_bucket     = var.create_s3_buckets ? aws_s3_bucket.guide_documents[0].bucket : data.aws_s3_bucket.documents[0].bucket

  # VPC
  azs_count          = var.azs_count
  create_vpc         = var.vpc_id == "" ? true : false
  vpc_id             = local.create_vpc ? module.vpc[0].vpc_id : var.vpc_id
  vpc_cidr           = local.create_vpc ? module.vpc[0].vpc_cidr_block : data.aws_vpc.this[0].cidr_block
  public_subnet_ids  = local.create_vpc ? module.vpc[0].public_subnets : data.aws_subnet_ids.public
  private_subnet_ids = local.create_vpc ? module.vpc[0].private_subnets : data.aws_subnet_ids.private

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
data "aws_subnet_ids" "public" {
  count = local.create_vpc ? 0 : 1

  vpc_id = var.vpc_id

  tags = {
    type = "public"
  }
}
data "aws_subnet_ids" "private" {
  count = local.create_vpc ? 0 : 1

  vpc_id = var.vpc_id

  tags = {
    type = "private"
  }
}

data "aws_kms_key" "rds" {
  key_id = var.rds_kms_key_id
}
data "aws_kms_key" "s3" {
  key_id = var.kms_key_id
}