// We use "moved" blocks to allow for upgrading from older version of the infra that used an older AWS provider as well
// as individual S3 bucket blocks. (don't be alarmed if your IDE marks these "old" resources as errors, they should be)

# Maintaining backwards compatibility to 3.1
moved {
  from = data.aws_s3_bucket.guide_images[0]
  to   = data.aws_s3_bucket.guide_buckets["image"]
}
moved {
  from = data.aws_s3_bucket.guide_documents[0]
  to   = data.aws_s3_bucket.guide_buckets["doc"]
}
moved {
  from = data.aws_s3_bucket.guide_objects[0]
  to   = data.aws_s3_bucket.guide_buckets["obj"]
}
moved {
  from = data.aws_s3_bucket.guide_pdfs[0]
  to   = data.aws_s3_bucket.guide_buckets["pdf"]
}
moved {
  from = aws_s3_bucket.guide_images[0]
  to   = aws_s3_bucket.guide_buckets["image"]
}
moved {
  from = aws_s3_bucket.guide_documents[0]
  to   = aws_s3_bucket.guide_buckets["doc"]
}
moved {
  from = aws_s3_bucket.guide_objects[0]
  to   = aws_s3_bucket.guide_buckets["obj"]
}
moved {
  from = aws_s3_bucket.guide_pdfs[0]
  to   = aws_s3_bucket.guide_buckets["pdf"]
}
moved {
  from = aws_s3_bucket_public_access_block.guide_images_acl_block[0]
  to   = aws_s3_bucket_public_access_block.guide_buckets_acl_block["image"]
}
moved {
  from = aws_s3_bucket_public_access_block.guide_documents_acl_block[0]
  to   = aws_s3_bucket_public_access_block.guide_buckets_acl_block["doc"]
}
moved {
  from = aws_s3_bucket_public_access_block.guide_objects_acl_block[0]
  to   = aws_s3_bucket_public_access_block.guide_buckets_acl_block["obj"]
}
moved {
  from = aws_s3_bucket_public_access_block.guide_pdfs_acl_block[0]
  to   = aws_s3_bucket_public_access_block.guide_buckets_acl_block["pdf"]
}
# - End backwards Compatibility

// If S3 key is provided by a variable, use that otherwise create a new one.
data "aws_kms_key" "s3" {
  count = var.s3_kms_key_id != "" ? 1 : 0

  key_id = var.s3_kms_key_id
}
resource "aws_kms_key" "s3_kms_key" {
  description             = "KMS key to encrypt S3 bucket contents"
  deletion_window_in_days = 7
}
resource "aws_kms_alias" "s3_kms_key" {
  name_prefix   = "alias/${local.identifier}/${data.aws_region.current.name}/s3/"
  target_key_id = aws_kms_key.s3_kms_key.id
}

// If using existing buckets
data "aws_s3_bucket" "guide_buckets" {

  // If not using existing buckets the local variable will be empty and the resource will not be created.
  // This loop says: for each entry in the s3_existing_buckets map, use the "type" attribute for the resource key
  // (i.e. v.type = "pdf" then aws_s3_bucket.guide_buckets["pdf"])
  for_each = { for k, v in local.s3_existing_buckets : v.type => v }

  bucket = each.value.bucket_name

  lifecycle {
    precondition {
      condition     = var.s3_kms_key_id != ""
      error_message = "To use existing buckets, you must specify the KMS key ID used to encrypt its contents."
    }
  }
}

# Begin S3 Replication for existing buckets
data "aws_iam_policy_document" "s3_replication_assume_role" {
  count = local.use_existing_buckets ? 1 : 0

  statement {
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["s3.${data.aws_partition.current.dns_suffix}", "batchoperations.s3.${data.aws_partition.current.dns_suffix}"]
    }

    actions = ["sts:AssumeRole"]
  }
}

resource "aws_iam_role" "s3_replication" {
  count = local.use_existing_buckets ? 1 : 0

  name               = "${local.identifier}-${data.aws_region.current.name}-s3-replication-role"
  assume_role_policy = data.aws_iam_policy_document.s3_replication_assume_role[0].json
}

data "aws_iam_policy_document" "s3_replication" {
  count = local.use_existing_buckets ? 1 : 0

  depends_on = [data.aws_s3_bucket.guide_buckets]

  statement {
    effect = "Allow"

    actions = [
      "s3:GetReplicationConfiguration",
      "s3:ListBucket",
      "s3:PutInventoryConfiguration"
    ]

    resources = local.s3_source_bucket_arn_list
  }

  statement {
    effect = "Allow"

    actions = [
      "s3:GetObjectVersionForReplication",
      "s3:GetObjectVersionAcl",
      "s3:GetObjectVersionTagging",
      "s3:InitiateReplication",
      "s3:GetObject"
    ]

    resources = local.s3_source_bucket_arn_list_with_objects
  }

  statement {
    effect = "Allow"

    actions = [
      "s3:ReplicateObject",
      "s3:ReplicateDelete",
      "s3:ReplicateTags",
      "s3:GetObject",
      "s3:GetObjectVersion",
      "s3:GetObjectVersionForReplication",
      "s3:PutObject"
    ]

    resources = local.s3_destination_bucket_arn_list_with_objects
  }

  statement {
    effect = "Allow"

    actions = ["s3:GetObject",
      "s3:GetObjectVersion",
    "s3:PutObject"]

    resources = ["${aws_s3_bucket.logging_bucket.arn}/*"]
  }

  statement {
    effect = "Allow"

    actions = ["kms:Decrypt"]

    condition {
      test     = "StringLike"
      variable = "kms:ViaService"
      values   = ["s3.${data.aws_region.current.name}.${data.aws_partition.current.dns_suffix}"]
    }
    condition {
      test     = "StringLike"
      variable = "kms:EncryptionContext:${data.aws_partition.current.partition}:s3:arn"
      values   = local.s3_source_bucket_arn_list_with_objects
    }

    resources = [data.aws_kms_key.s3[0].arn]
  }

  statement {
    effect = "Allow"

    actions = ["kms:Encrypt"]

    condition {
      test     = "StringLike"
      variable = "kms:ViaService"
      values   = ["s3.${data.aws_region.current.name}.${data.aws_partition.current.dns_suffix}"]
    }
    condition {
      test     = "StringLike"
      variable = "kms:EncryptionContext:${data.aws_partition.current.partition}:s3:arn"
      values   = local.s3_destination_bucket_arn_list_with_objects
    }

    resources = [aws_kms_key.s3_kms_key.arn]
  }
}

resource "aws_iam_policy" "s3_replication" {
  count = local.use_existing_buckets ? 1 : 0

  name   = "${local.identifier}-${data.aws_region.current.name}-s3-replication-policy"
  policy = data.aws_iam_policy_document.s3_replication[0].json
}

resource "aws_iam_role_policy_attachment" "s3_replication" {
  count = local.use_existing_buckets ? 1 : 0

  role       = aws_iam_role.s3_replication[0].name
  policy_arn = aws_iam_policy.s3_replication[0].arn
}

resource "aws_s3_bucket_replication_configuration" "replication" {
  for_each = { for k, v in local.existing_bucket_map : v.type => v }

  role   = aws_iam_role.s3_replication[0].arn
  bucket = each.value.source

  rule {
    status = "Enabled"

    source_selection_criteria {
      sse_kms_encrypted_objects {
        status = "Enabled"
      }
    }

    destination {
      bucket        = each.value.destination
      storage_class = "STANDARD"
      encryption_configuration {
        replica_kms_key_id = aws_kms_key.s3_kms_key.arn
      }
    }
  }
}

resource "null_resource" "s3_replication_job_init" {
  for_each = { for k, v in local.existing_bucket_map : v.type => v }

  triggers = {
    aws_account      = data.aws_caller_identity.current.account_id
    aws_profile      = var.aws_profile
    logging_bucket   = aws_s3_bucket.logging_bucket.arn
    source_bucket    = each.value.source
    replication_role = aws_iam_role.s3_replication[0].arn
  }

  provisioner "local-exec" {
    command = "/usr/bin/env bash ./util/create-s3-batch.sh ${self.triggers["logging_bucket"]} ${self.triggers["source_bucket"]} ${self.triggers["replication_role"]} ${self.triggers["aws_account"]} ${self.triggers["aws_profile"]}"
  }
}

# - End S3 Replication

resource "aws_s3_bucket_policy" "logging_policy" {
  bucket = aws_s3_bucket.logging_bucket.id
  policy = data.aws_iam_policy_document.logging_policy.json
}

data "aws_iam_policy_document" "logging_policy" {
  statement {
    principals {
      type        = "Service"
      identifiers = ["logging.s3.amazonaws.com"]
    }

    actions = [
      "s3:PutObject"
    ]

    resources = [
      aws_s3_bucket.logging_bucket.arn,
      "${aws_s3_bucket.logging_bucket.arn}/*",
    ]
  }
}

resource "aws_s3_bucket_public_access_block" "logging_bucket_acl_block" {

  bucket = aws_s3_bucket.logging_bucket.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_acl" "logging_bucket_acl" {

  bucket = aws_s3_bucket.logging_bucket.id

  acl = "log-delivery-write"
}

resource "aws_s3_bucket_versioning" "logging_bucket_versioning_block" {

  bucket = aws_s3_bucket.logging_bucket.id

  versioning_configuration {
    status = "Enabled"
  }
}

// Let's disable logging on the logging bucket to prevent creating a black hole that destroys the universe.
#tfsec:ignore:aws-s3-enable-bucket-logging
resource "aws_s3_bucket" "logging_bucket" {

  bucket_prefix = "${local.identifier}-log-${data.aws_region.current.name}"
  force_destroy = !var.protect_resources
}

resource "aws_s3_bucket_server_side_encryption_configuration" "logging_bucket_encryption" {

  bucket = aws_s3_bucket.logging_bucket.bucket

  rule {
    bucket_key_enabled = false
    apply_server_side_encryption_by_default {
      kms_master_key_id = aws_kms_key.s3_kms_key.arn
      sse_algorithm     = "aws:kms"
    }
  }
}

# Begin creating buckets and associated bucket resources dynamically
resource "aws_s3_bucket" "guide_buckets" {
  for_each = toset(local.create_s3_bucket_names)

  bucket_prefix = "${local.identifier}-${each.key}-${data.aws_region.current.name}"
  force_destroy = !var.protect_resources
}
resource "aws_s3_bucket_logging" "guide_buckets_logging" {
  for_each = toset(local.create_s3_bucket_names)

  bucket = lookup(lookup(aws_s3_bucket.guide_buckets, each.key), "bucket")

  target_bucket = aws_s3_bucket.logging_bucket.bucket
  target_prefix = "${each.key}/"
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
    bucket_key_enabled = false
    apply_server_side_encryption_by_default {
      kms_master_key_id = aws_kms_key.s3_kms_key.arn
      sse_algorithm     = "aws:kms"
    }
  }

}

resource "aws_s3_bucket_cors_configuration" "guide_documents" {

  bucket = aws_s3_bucket.guide_buckets["doc"].id

  cors_rule {
    allowed_methods = ["GET"]
    allowed_origins = ["*"]
    allowed_headers = ["Authorization", "Range"]
    expose_headers  = ["Accept-Ranges", "Content-Encoding", "Content-Length", "Content-Range"]
    max_age_seconds = 3000
  }
}

# - End dynamic S3 resource creation