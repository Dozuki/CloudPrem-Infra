terraform {
  backend "s3" {}

  required_version = "0.14.10"

  required_providers {
    aws        = "3.22.0"
    random     = "3.0.0"
    kubernetes = "1.13.3"
    helm       = "2.1.2"
    time       = "0.6.0"
    # null       = "2.1.2"
  }
}

#  ########### Providers ###########

provider "aws" {
  region = var.region
}

#  ############# Locals ############

locals {
  identifier = var.identifier == "" ? "cloudprem-${var.environment}" : "${var.identifier}-cloudprem-${var.environment}"

  dozuki_license_parameter_name = var.dozuki_license_parameter_name != "" ? var.dozuki_license_parameter_name : var.identifier == "" ? "/cloudprem/${var.environment}/license" : "/${var.identifier}/cloudprem/${var.environment}/license"

  tags = {
    Terraform   = "true"
    Project     = "cloudprem"
    Identifier  = var.identifier
    Environment = var.environment
  }

  protect_resources = var.stack_type == "production" ? true : false

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  # Networking
  azs_count          = 3
  create_vpc         = var.vpc_id == "" ? true : false
  vpc_id             = local.create_vpc ? module.vpc[0].vpc_id : var.vpc_id
  vpc_cidr           = local.create_vpc ? module.vpc[0].vpc_cidr_block : data.aws_vpc.this[0].cidr_block
  public_subnet_ids  = local.create_vpc ? module.vpc[0].public_subnets : data.aws_subnet_ids.public
  private_subnet_ids = local.create_vpc ? module.vpc[0].private_subnets : data.aws_subnet_ids.private

  # Database
  ca_cert_identifier = local.is_us_gov ? "rds-ca-2017" : "rds-ca-2019"

  # S3 Buckets
  guide_images_bucket = var.create_s3_buckets ? module.guide_images_s3_bucket.this_s3_bucket_id : data.aws_s3_bucket.guide_images[0].bucket
  guide_objects_bucket = var.create_s3_buckets ? module.guide_objects_s3_bucket.this_s3_bucket_id : data.aws_s3_bucket.guide_objects[0].bucket
  guide_pdfs_bucket = var.create_s3_buckets ? module.guide_pdfs_s3_bucket.this_s3_bucket_id : data.aws_s3_bucket.guide_pdfs[0].bucket
  documents_bucket = var.create_s3_buckets ? module.documents_s3_bucket.this_s3_bucket_id : data.aws_s3_bucket.documents[0].bucket

}

#  ########### Resources ###########

data "aws_partition" "current" {}

data "aws_region" "current" {}

data "aws_availability_zones" "available" {}

data "aws_caller_identity" "current" {}

data "aws_vpc" "this" {
  count = local.create_vpc ? 0 : 1
  id = var.vpc_id
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

data "aws_kms_key" "s3" {
  key_id = var.s3_kms_key_id
}

data "aws_s3_bucket" "guide_images" {
  count = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_images_bucket
}
data "aws_s3_bucket" "guide_objects" {
  count = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_objects_bucket
}
data "aws_s3_bucket" "guide_pdfs" {
  count = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_pdfs_bucket
}
data "aws_s3_bucket" "documents" {
  count = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_documents_bucket
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "3.0.0"

  count = local.create_vpc ? 1 : 0

  name = local.identifier
  cidr = var.vpc_cidr
  azs  = slice(data.aws_availability_zones.available.names, 0, 3)

  enable_nat_gateway     = true
  single_nat_gateway     = !var.highly_available_nat_gateway
  one_nat_gateway_per_az = true
  enable_dns_hostnames   = true

  public_subnets = [for i in range(local.azs_count) : cidrsubnet(var.vpc_cidr, 4, i)]

  public_subnet_tags = {
    "kubernetes.io/cluster/${local.identifier}" = "shared"
    "kubernetes.io/role/elb"                    = "1"
    "type"                                      = "public"
  }

  private_subnets = [for i in range(local.azs_count, local.azs_count * 2) : cidrsubnet(var.vpc_cidr, 4, i)]

  private_subnet_tags = {
    "kubernetes.io/cluster/${local.identifier}" = "shared"
    "kubernetes.io/role/internal-elb"           = "1"
    "type"                                      = "private"
  }

  tags = local.tags
}

module "guide_images_s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "1.16.0"

  create_bucket = var.create_s3_buckets

  bucket_prefix = "dozuki-guide-images"
  acl           = "private"
  force_destroy = !local.protect_resources

  # S3 bucket-level Public Access Block configuration
  block_public_acls   = !var.public_access
  block_public_policy = !var.public_access

  versioning = {
    enabled = true
  }

  server_side_encryption_configuration = {

    rule = {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default = {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }

  }

  tags = local.tags
}

module "guide_pdfs_s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "1.16.0"

  //  count = var.create_s3_buckets ? 1 : 0
  create_bucket = var.create_s3_buckets

  bucket_prefix = "dozuki-guide-pdfs"
  acl           = "private"
  force_destroy = !local.protect_resources

  # S3 bucket-level Public Access Block configuration
  block_public_acls   = !var.public_access
  block_public_policy = !var.public_access

  versioning = {
    enabled = true
  }

  server_side_encryption_configuration = {
    rule = {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default = {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }
  }

  tags = local.tags
}

module "guide_objects_s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "1.16.0"

  //  count = var.create_s3_buckets ? 1 : 0
  create_bucket = var.create_s3_buckets

  bucket_prefix = "dozuki-guide-objects"
  acl           = "private"
  force_destroy = !local.protect_resources

  # S3 bucket-level Public Access Block configuration
  block_public_acls   = !var.public_access
  block_public_policy = !var.public_access

  versioning = {
    enabled = true
  }

  server_side_encryption_configuration = {
    rule = {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default = {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }
  }

  tags = local.tags
}

module "documents_s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "1.16.0"

//  count = var.create_s3_buckets ? 1 : 0
  create_bucket = var.create_s3_buckets

  bucket_prefix = "dozuki-documents"
  acl           = "private"
  force_destroy = !local.protect_resources

  # S3 bucket-level Public Access Block configuration
  block_public_acls   = !var.public_access
  block_public_policy = !var.public_access

  versioning = {
    enabled = true
  }

  server_side_encryption_configuration = {
    rule = {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default = {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }
  }

  cors_rule = [
    {
      allowed_methods = ["GET"]
      allowed_origins = ["*"]
      allowed_headers = ["Authorization", "Range"]
      expose_headers  = ["Accept-Ranges", "Content-Encoding", "Content-Length", "Content-Range"]
      max_age_seconds = 3000
    }
  ]

  tags = local.tags
}

module "memcached" {
  source = "./modules/elasticache-memcached"

  name = local.identifier

  vpc_id     = local.vpc_id
  subnet_ids = local.private_subnet_ids

  allowed_cidr_blocks = [local.vpc_cidr]

  cluster_size  = 1
  instance_type = var.cache_instance_type
}
module "webhooks" {
  source = "./modules/webhooks"

  count = var.enable_webhooks ? 1 : 0

//  depends_on = [module.eks_cluster]

  name = local.identifier

  vpc_id     = local.vpc_id
  subnet_ids = local.private_subnet_ids

  allowed_cidr_blocks = [local.vpc_cidr]

  cluster_size  = local.azs_count
  rds_kms_key_id = var.rds_kms_key_id

  rds_address = module.primary_database.this_db_instance_address
  rds_user = module.primary_database.this_db_instance_username
  rds_pass = random_password.primary_database.result
  eks_sg = module.eks_cluster.cluster_primary_security_group_id
  frontegg_api_key = var.frontegg_api_key
  frontegg_client_id = var.frontegg_client_id
  frontegg_secret = data.kubernetes_secret.frontegg[0]
  eks_cluster = module.eks_cluster

  tags = local.tags
}


module "sns" {
  source  = "terraform-aws-modules/sns/aws"
  version = "2.1.0"
  name = local.identifier
}