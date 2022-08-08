// We use "moved" blocks to allow for upgrading from older version of the infra that used an older AWS provider as well
// as individual S3 bucket blocks. (don't be alarmed if your IDE marks these resources as errors, they should be)
moved {
  from = data.aws_s3_bucket.guide_images[0]
  to   = data.aws_s3_bucket.guide_buckets["dozuki-guide-images"]
}
moved {
  from = data.aws_s3_bucket.guide_documents[0]
  to   = data.aws_s3_bucket.guide_buckets["dozuki-guide-documents"]
}
moved {
  from = data.aws_s3_bucket.guide_objects[0]
  to   = data.aws_s3_bucket.guide_buckets["dozuki-guide-objects"]
}
moved {
  from = data.aws_s3_bucket.guide_pdfs[0]
  to   = data.aws_s3_bucket.guide_buckets["dozuki-guide-pdfs"]
}
moved {
  from = aws_s3_bucket.guide_images[0]
  to   = aws_s3_bucket.guide_buckets["dozuki-guide-images"]
}
moved {
  from = aws_s3_bucket.guide_documents[0]
  to   = aws_s3_bucket.guide_buckets["dozuki-guide-documents"]
}
moved {
  from = aws_s3_bucket.guide_objects[0]
  to   = aws_s3_bucket.guide_buckets["dozuki-guide-objects"]
}
moved {
  from = aws_s3_bucket.guide_pdfs[0]
  to   = aws_s3_bucket.guide_buckets["dozuki-guide-pdfs"]
}
moved {
  from = aws_s3_bucket_public_access_block.guide_images_acl_block[0]
  to   = aws_s3_bucket_public_access_block.guide_buckets_acl_block["dozuki-guide-images"]
}
moved {
  from = aws_s3_bucket_public_access_block.guide_documents_acl_block[0]
  to   = aws_s3_bucket_public_access_block.guide_buckets_acl_block["dozuki-guide-documents"]
}
moved {
  from = aws_s3_bucket_public_access_block.guide_objects_acl_block[0]
  to   = aws_s3_bucket_public_access_block.guide_buckets_acl_block["dozuki-guide-objects"]
}
moved {
  from = aws_s3_bucket_public_access_block.guide_pdfs_acl_block[0]
  to   = aws_s3_bucket_public_access_block.guide_buckets_acl_block["dozuki-guide-pdfs"]
}

// If using existing buckets
data "aws_s3_bucket" "guide_buckets" {
  for_each = toset(local.existing_s3_bucket_names)
  bucket   = each.key
}
data "aws_s3_bucket" "logging" {
  count  = var.create_s3_buckets ? 0 : 1
  bucket = var.s3_logging_bucket
}

// If creating new buckets
resource "aws_s3_bucket_public_access_block" "logging_bucket_acl_block" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = aws_s3_bucket.logging_bucket[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_acl" "logging_bucket_acl" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = aws_s3_bucket.logging_bucket[0].id

  acl = "private"
}
resource "aws_s3_bucket_versioning" "logging_bucket_versioning_block" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = aws_s3_bucket.logging_bucket[0].id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "logging_bucket_encryption_block" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = aws_s3_bucket.logging_bucket[0].id

  rule {
    bucket_key_enabled = true
    apply_server_side_encryption_by_default {
      kms_master_key_id = var.s3_kms_key_id
      sse_algorithm     = "aws:kms"
    }
  }
}

# Let's disable logging on the logging bucket to prevent creating a blackhole that destroys the universe.
#tfsec:ignore:aws-s3-enable-bucket-logging
resource "aws_s3_bucket" "logging_bucket" {
  count = var.create_s3_buckets ? 1 : 0

  bucket        = "dozuki-bucket-access-logs-${local.identifier}-${data.aws_region.current.name}"
  force_destroy = !var.protect_resources
}

# Begin creating buckets dynamically
resource "aws_s3_bucket" "guide_buckets" {
  for_each = toset(local.create_s3_bucket_names)

  bucket        = "${each.key}-${local.identifier}-${data.aws_region.current.name}"
  force_destroy = !var.protect_resources
}
resource "aws_s3_bucket_logging" "guide_buckets_logging" {
  for_each = aws_s3_bucket.guide_buckets

  bucket = each.value.id

  target_bucket = local.logging_bucket
  target_prefix = each.value.id
}
resource "aws_s3_bucket_public_access_block" "guide_buckets_acl_block" {
  for_each = aws_s3_bucket.guide_buckets

  bucket = each.value.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_acl" "guide_buckets_acl" {
  for_each = aws_s3_bucket.guide_buckets

  bucket = each.value.id
  acl    = "private"
}
resource "aws_s3_bucket_versioning" "guide_buckets_versioning" {
  for_each = aws_s3_bucket.guide_buckets

  bucket = each.value.id

  versioning_configuration {
    status = "Enabled"
  }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "guide_buckets_encryption" {
  for_each = aws_s3_bucket.guide_buckets

  bucket = each.value.id

  rule {
    bucket_key_enabled = true
    apply_server_side_encryption_by_default {
      kms_master_key_id = var.s3_kms_key_id
      sse_algorithm     = "aws:kms"
    }
  }
}

resource "aws_s3_bucket_cors_configuration" "guide_documents" {
  count = var.create_s3_buckets ? 1 : 0

  bucket = local.guide_buckets["dozuki-guide-documents"].id

  cors_rule {
    allowed_methods = ["GET"]
    allowed_origins = ["*"]
    allowed_headers = ["Authorization", "Range"]
    expose_headers  = ["Accept-Ranges", "Content-Encoding", "Content-Length", "Content-Range"]
    max_age_seconds = 3000
  }
}