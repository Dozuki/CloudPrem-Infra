#tfsec:ignore:aws-s3-enable-bucket-logging
resource "aws_s3_bucket" "vpn-config-files" {
  bucket        = "${local.identifier}-${data.aws_region.current.name}-vpn-credentials"
  force_destroy = true

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

resource "aws_s3_bucket_public_access_block" "vpn-config-files" {
  bucket                  = aws_s3_bucket.vpn-config-files.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_policy" "vpn-config-files" {
  bucket = aws_s3_bucket.vpn-config-files.id
  policy = data.aws_iam_policy_document.vpn-config-files.json
}

data "aws_iam_policy_document" "vpn-config-files" {
  statement {
    actions = ["s3:*"]
    effect  = "Deny"
    resources = [
      "arn:${data.aws_partition.current.partition}:s3:::${local.identifier}-${data.aws_region.current.name}-vpn-credentials",
      "arn:${data.aws_partition.current.partition}:s3:::${local.identifier}-${data.aws_region.current.name}-vpn-credentials/*"
    ]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
    principals {
      type        = "*"
      identifiers = ["*"]
    }
  }
}