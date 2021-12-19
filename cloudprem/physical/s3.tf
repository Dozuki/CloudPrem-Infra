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
resource "aws_s3_bucket_public_access_block" "logging_bucket_acl_block" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = aws_s3_bucket.logging_bucket[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Let's disable logging on the logging bucket to prevent creating a blackhole that destroys the universe.
#tfsec:ignore:aws-s3-enable-bucket-logging
resource "aws_s3_bucket" "logging_bucket" {
  count = var.create_s3_buckets ? 1 : 0

  bucket_prefix = "dozuki-bucket-access-logs"
  acl           = "private"
  force_destroy = !var.protect_resources

  versioning {
    enabled = true
  }

  server_side_encryption_configuration {

    rule {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }

  }
}
#resource "aws_s3_bucket_public_access_block" "guide_images_acl_block" {
#  count  = var.create_s3_buckets ? 1 : 0
#
#  bucket = aws_s3_bucket.guide_images[0].id
#
#  block_public_acls   = true
#  block_public_policy = true
##  ignore_public_acls = true
##  restrict_public_buckets = true
#}
resource "aws_s3_bucket" "guide_images" {
  count = var.create_s3_buckets ? 1 : 0

  bucket_prefix = "dozuki-guide-images"
  acl           = "public-read"
  force_destroy = !var.protect_resources

  versioning {
    enabled = true
  }

  logging {
    target_bucket = aws_s3_bucket.logging_bucket[0].id
    target_prefix = "guide-images"
  }

  server_side_encryption_configuration {

    rule {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }

  }
}
resource "aws_s3_bucket_public_access_block" "guide_objects_acl_block" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = aws_s3_bucket.guide_objects[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket" "guide_objects" {
  count = var.create_s3_buckets ? 1 : 0

  bucket_prefix = "dozuki-guide-objects"
  acl           = "private"
  force_destroy = !var.protect_resources

  versioning {
    enabled = true
  }

  logging {
    target_bucket = aws_s3_bucket.logging_bucket[0].id
    target_prefix = "guide-objects"
  }

  server_side_encryption_configuration {

    rule {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }

  }
}
resource "aws_s3_bucket_public_access_block" "guide_pdfs_acl_block" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = aws_s3_bucket.guide_pdfs[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket" "guide_pdfs" {
  count = var.create_s3_buckets ? 1 : 0

  bucket_prefix = "dozuki-guide-pdfs"
  acl           = "private"
  force_destroy = !var.protect_resources

  versioning {
    enabled = true
  }

  logging {
    target_bucket = aws_s3_bucket.logging_bucket[0].id
    target_prefix = "guide-pdfs"
  }

  server_side_encryption_configuration {

    rule {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }

  }
}
resource "aws_s3_bucket_public_access_block" "guide_documents_acl_block" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = aws_s3_bucket.guide_documents[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket" "guide_documents" {
  count = var.create_s3_buckets ? 1 : 0

  bucket_prefix = "dozuki-guide-documents"
  acl           = "private"
  force_destroy = !var.protect_resources

  versioning {
    enabled = true
  }

  logging {
    target_bucket = aws_s3_bucket.logging_bucket[0].id
    target_prefix = "guide-documents"
  }

  server_side_encryption_configuration {

    rule {
      bucket_key_enabled = true
      apply_server_side_encryption_by_default {
        kms_master_key_id = var.s3_kms_key_id
        sse_algorithm     = "aws:kms"
      }
    }

  }

  cors_rule {
    allowed_methods = ["GET"]
    allowed_origins = ["*"]
    allowed_headers = ["Authorization", "Range"]
    expose_headers  = ["Accept-Ranges", "Content-Encoding", "Content-Length", "Content-Range"]
    max_age_seconds = 3000
  }

}