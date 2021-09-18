terraform {
  required_providers {
    aws        = "3.56.0"
    random     = "3.1.0"
  }
}

locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-dozuki-${var.environment}"

 tags = {
    Terraform = "true"
    Project = "Dozuki"
    Identifier = var.identifier
    Environment = var.environment
  }

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  # Database
  ca_cert_identifier = local.is_us_gov ? "rds-ca-2017" : "rds-ca-2019"

  # S3 Buckets
  guide_images_bucket = var.create_s3_buckets ? module.guide_images_s3_bucket.s3_bucket_id : data.aws_s3_bucket.guide_images[0].bucket
  guide_objects_bucket = var.create_s3_buckets ? module.guide_objects_s3_bucket.s3_bucket_id : data.aws_s3_bucket.guide_objects[0].bucket
  guide_pdfs_bucket = var.create_s3_buckets ? module.guide_pdfs_s3_bucket.s3_bucket_id : data.aws_s3_bucket.guide_pdfs[0].bucket
  documents_bucket = var.create_s3_buckets ? module.documents_s3_bucket.s3_bucket_id : data.aws_s3_bucket.documents[0].bucket

}

data "aws_partition" "current" {}

data "aws_region" "current" {}

data "aws_kms_key" "rds" {
  key_id = var.rds_kms_key_id
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

