data "aws_s3_bucket" "guide_images" {
  count  = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_images_bucket
}
data "aws_s3_bucket" "guide_objects" {
  count  = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_objects_bucket
}
data "aws_s3_bucket" "guide_pdfs" {
  count  = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_pdfs_bucket
}
data "aws_s3_bucket" "documents" {
  count  = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_documents_bucket
}

# We need to allow customers to create public buckets so these security issues must be ignored
#tfsec:ignore:aws-s3-enable-bucket-logging
#tfsec:ignore:aws-s3-block-public-acls
#tfsec:ignore:aws-s3-ignore-public-acls
#tfsec:ignore:aws-s3-block-public-policy
#tfsec:ignore:aws-s3-no-public-buckets
module "guide_images_s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "2.9.0"

  create_bucket = var.create_s3_buckets

  bucket_prefix = "dozuki-guide-images"
  acl           = "private"
  force_destroy = !var.protect_resources

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

#tfsec:ignore:aws-s3-enable-bucket-logging
#tfsec:ignore:aws-s3-block-public-acls
#tfsec:ignore:aws-s3-ignore-public-acls
#tfsec:ignore:aws-s3-block-public-policy
#tfsec:ignore:aws-s3-no-public-buckets
module "guide_pdfs_s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "2.9.0"

  create_bucket = var.create_s3_buckets

  bucket_prefix = "dozuki-guide-pdfs"
  acl           = "private"
  force_destroy = !var.protect_resources

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

#tfsec:ignore:aws-s3-enable-bucket-logging
#tfsec:ignore:aws-s3-block-public-acls
#tfsec:ignore:aws-s3-ignore-public-acls
#tfsec:ignore:aws-s3-block-public-policy
#tfsec:ignore:aws-s3-no-public-buckets
module "guide_objects_s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "2.9.0"

  create_bucket = var.create_s3_buckets

  bucket_prefix = "dozuki-guide-objects"
  acl           = "private"
  force_destroy = !var.protect_resources

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

#tfsec:ignore:aws-s3-enable-bucket-logging
#tfsec:ignore:aws-s3-block-public-acls
#tfsec:ignore:aws-s3-ignore-public-acls
#tfsec:ignore:aws-s3-block-public-policy
#tfsec:ignore:aws-s3-no-public-buckets
module "documents_s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "2.9.0"

  create_bucket = var.create_s3_buckets

  bucket_prefix = "dozuki-documents"
  acl           = "private"
  force_destroy = !var.protect_resources

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
