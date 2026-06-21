# Disaster Recovery — Phase 1 (cold tier).
# Always-on cross-region replication of the two stateful stores (primary RDS,
# S3 content buckets) into var.dr_region, gated on var.enable_dr. The DR region
# is resolved by the Spacelift admin layer and injected as TG_AWS_DR_REGION, so
# var.dr_region arrives concrete (see the DR Phase 1 spec). All resources here
# use the Terragrunt-generated `aws.dr` provider.

locals {
  dr_enabled = var.enable_dr

  # Source RDS instance ARN (the rds module exposes no ARN output, so construct it).
  dr_source_db_arn = "arn:${data.aws_partition.current.partition}:rds:${data.aws_region.current.id}:${data.aws_caller_identity.current.account_id}:db:${local.identifier}"

  # Use a Terraform-created customer-managed key for RDS only when a new stack
  # opts in AND no explicit key was given. Resolves to the SAME ARN as before for
  # existing stacks (flag false), so it never triggers a DB replacement.
  rds_use_dr_cmk  = var.enable_dr && var.rds_adopt_dr_cmk && var.rds_kms_key_id == "alias/aws/rds"
  rds_kms_key_arn = local.rds_use_dr_cmk ? aws_kms_key.rds_cmk[0].arn : data.aws_kms_key.rds.arn

  # RDS automated-backup cross-region replication is only possible when the DB
  # uses a customer-managed key (either our created CMK or an operator-pinned one).
  # Aurora DR uses Global Database (Plan B), not automated-backup replication.
  dr_rds_enabled = var.db_engine == "rds" && var.enable_dr && (local.rds_use_dr_cmk || data.aws_kms_key.rds.key_manager == "CUSTOMER")
}

# Defense-in-depth guardrail. The real selection + blocklist enforcement happens
# in the admin layer; this only catches a missing/echoed injection.
check "dr_region_valid" {
  assert {
    condition     = !var.enable_dr || (var.dr_region != "" && var.dr_region != data.aws_region.current.id)
    error_message = "enable_dr is true but dr_region is empty or equals the primary region (${data.aws_region.current.id}). The admin layer must inject TG_AWS_DR_REGION, or set dr_region explicitly."
  }
}

# Non-blocking warning: surfaced on every plan/apply when DR is on but the DB
# uses an AWS-managed key, so RDS automated backups are NOT being replicated.
# S3 content IS still replicated. Migrate the DB to a customer-managed key
# (new stacks: rds_adopt_dr_cmk = true; or set rds_kms_key_id to a CMK).
check "dr_rds_replicable" {
  assert {
    condition     = !var.enable_dr || local.dr_rds_enabled
    error_message = "DR is enabled but the RDS instance uses an AWS-managed KMS key; its automated backups are NOT being replicated cross-region (S3 content IS). To enable RDS DR, the database must use a customer-managed key — see the DR cold-recovery runbook."
  }
}

# Customer-managed key for the PRIMARY RDS instance, created only when a new
# stack opts in (rds_adopt_dr_cmk). Required so automated backups are eligible
# for cross-region replication. Never created for existing managed-key stacks.
resource "aws_kms_key" "rds_cmk" {
  count = local.rds_use_dr_cmk ? 1 : 0

  description             = "${local.identifier} RDS encryption (DR-replicable)"
  enable_key_rotation     = true
  deletion_window_in_days = var.protect_resources ? 30 : 7
  tags                    = local.tags
}

resource "aws_kms_alias" "rds_cmk" {
  count = local.rds_use_dr_cmk ? 1 : 0

  name_prefix   = "alias/${local.identifier}/rds-dr/"
  target_key_id = aws_kms_key.rds_cmk[0].id
}

# DR-region CMK for replicated RDS automated backups (the encrypted source DB
# requires a destination-region key).
resource "aws_kms_key" "dr_rds" {
  count    = local.dr_rds_enabled ? 1 : 0
  provider = aws.dr

  description             = "${local.identifier} DR replicated RDS backups"
  enable_key_rotation     = true
  deletion_window_in_days = var.protect_resources ? 30 : 7
  tags                    = local.tags
}

resource "aws_kms_alias" "dr_rds" {
  count    = local.dr_rds_enabled ? 1 : 0
  provider = aws.dr

  name_prefix   = "alias/${local.identifier}/dr/rds/"
  target_key_id = aws_kms_key.dr_rds[0].id
}

# DR-region CMK for the destination S3 buckets.
resource "aws_kms_key" "dr_s3" {
  count    = local.dr_enabled ? 1 : 0
  provider = aws.dr

  description             = "${local.identifier} DR replicated S3 content"
  enable_key_rotation     = true
  deletion_window_in_days = var.protect_resources ? 30 : 7
  tags                    = local.tags
}

resource "aws_kms_alias" "dr_s3" {
  count    = local.dr_enabled ? 1 : 0
  provider = aws.dr

  name_prefix   = "alias/${local.identifier}/dr/s3/"
  target_key_id = aws_kms_key.dr_s3[0].id
}

# Continuous cross-region replication of the primary DB's automated backups.
# Created in the DR region (aws.dr), pointing at the source DB ARN. PITR becomes
# available in the DR region. retention_period defaults to 7 days — recovery
# restores to the latest point, so there's no need to match the primary's
# (potentially 30d) retention and 4x the replicated-backup storage.
resource "aws_db_instance_automated_backups_replication" "primary" {
  count    = local.dr_rds_enabled ? 1 : 0
  provider = aws.dr

  source_db_instance_arn = local.dr_source_db_arn
  kms_key_id             = aws_kms_key.dr_rds[0].arn

  depends_on = [module.primary_database]
}

# Destination buckets for S3 cross-region replication, one per content bucket.
resource "aws_s3_bucket" "dr_guide_buckets" {
  for_each = local.dr_enabled ? aws_s3_bucket.guide_buckets : {}
  provider = aws.dr

  bucket_prefix = "${local.identifier}-${each.key}-dr-"
  tags          = local.tags

  lifecycle {
    ignore_changes = [bucket, bucket_prefix]
  }
}

resource "aws_s3_bucket_versioning" "dr_guide_buckets" {
  for_each = aws_s3_bucket.dr_guide_buckets
  provider = aws.dr

  bucket = each.value.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "dr_guide_buckets" {
  for_each = aws_s3_bucket.dr_guide_buckets
  provider = aws.dr

  bucket = each.value.id
  rule {
    apply_server_side_encryption_by_default {
      kms_master_key_id = aws_kms_key.dr_s3[0].arn
      sse_algorithm     = "aws:kms"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "dr_guide_buckets" {
  for_each = aws_s3_bucket.dr_guide_buckets
  provider = aws.dr

  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

data "aws_iam_policy_document" "dr_s3_replication_assume" {
  count = local.dr_enabled ? 1 : 0
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["s3.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "dr_s3_replication" {
  count              = local.dr_enabled ? 1 : 0
  name               = "${local.identifier}-${data.aws_region.current.id}-dr-s3-replication"
  assume_role_policy = data.aws_iam_policy_document.dr_s3_replication_assume[0].json
  tags               = local.tags
}

data "aws_iam_policy_document" "dr_s3_replication" {
  count = local.dr_enabled ? 1 : 0

  statement {
    effect    = "Allow"
    actions   = ["s3:GetReplicationConfiguration", "s3:ListBucket"]
    resources = [for b in aws_s3_bucket.guide_buckets : b.arn]
  }
  statement {
    effect    = "Allow"
    actions   = ["s3:GetObjectVersionForReplication", "s3:GetObjectVersionAcl", "s3:GetObjectVersionTagging"]
    resources = [for b in aws_s3_bucket.guide_buckets : "${b.arn}/*"]
  }

  statement {
    effect    = "Allow"
    actions   = ["s3:ReplicateObject", "s3:ReplicateDelete", "s3:ReplicateTags", "s3:ObjectOwnerOverrideToBucketOwner"]
    resources = [for b in aws_s3_bucket.dr_guide_buckets : "${b.arn}/*"]
  }

  statement {
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = distinct(compact([local.s3_kms_key_id, var.s3_kms_key_id]))
  }

  statement {
    effect    = "Allow"
    actions   = ["kms:Encrypt", "kms:GenerateDataKey"]
    resources = [aws_kms_key.dr_s3[0].arn]
  }
}

resource "aws_iam_policy" "dr_s3_replication" {
  count  = local.dr_enabled ? 1 : 0
  name   = "${local.identifier}-${data.aws_region.current.id}-dr-s3-replication"
  policy = data.aws_iam_policy_document.dr_s3_replication[0].json
}

resource "aws_iam_role_policy_attachment" "dr_s3_replication" {
  count      = local.dr_enabled ? 1 : 0
  role       = aws_iam_role.dr_s3_replication[0].name
  policy_arn = aws_iam_policy.dr_s3_replication[0].arn
}

# CRR from each source content bucket to its DR counterpart. Source versioning
# is already enabled (aws_s3_bucket_versioning.guide_buckets_versioning).
resource "aws_s3_bucket_replication_configuration" "dr" {
  for_each = aws_s3_bucket.dr_guide_buckets

  role   = aws_iam_role.dr_s3_replication[0].arn
  bucket = aws_s3_bucket.guide_buckets[each.key].id

  rule {
    id     = "dr-${each.key}"
    status = "Enabled"

    filter {}

    delete_marker_replication {
      status = "Enabled"
    }

    source_selection_criteria {
      sse_kms_encrypted_objects {
        status = "Enabled"
      }
    }

    destination {
      bucket        = each.value.arn
      storage_class = "STANDARD"
      encryption_configuration {
        replica_kms_key_id = aws_kms_key.dr_s3[0].arn
      }
    }
  }

  depends_on = [
    aws_iam_role_policy_attachment.dr_s3_replication,
    aws_s3_bucket_versioning.guide_buckets_versioning,
    aws_s3_bucket_versioning.dr_guide_buckets,
  ]
}
