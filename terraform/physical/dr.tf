# Disaster Recovery — Phase 1 (cold tier).
# Always-on cross-region replication of the two stateful stores (primary RDS,
# S3 content buckets) into var.dr_region, gated on var.enable_dr. The DR region
# is resolved by the Spacelift admin layer and injected as TG_AWS_DR_REGION, so
# var.dr_region arrives concrete (see the DR Phase 1 spec). All resources here
# use the Terragrunt-generated `aws.dr` provider.

locals {
  dr_enabled = var.enable_dr

  # Source RDS instance ARN (the rds module exposes no ARN output, so construct it).
  dr_source_db_arn = "arn:${data.aws_partition.current.partition}:rds:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:db:${local.identifier}"

  # A Terraform-created customer-managed key is the DEFAULT for the database. There is no
  # longer any second gate: rds_adopt_dr_cmk defaults true, so a fresh stack gets a CMK
  # whether or not cross-region DR is on.
  #
  # It used to be gated on enable_dr (later overridable by rds_create_cmk), which conflated two
  # separable things: which key encrypts the database, and whether DR is on. That conflation was
  # a one-way door. The KMS key is immutable on RDS and Aurora, so a stack that deferred DR was
  # silently born on the AWS-managed key and could then adopt a CMK only by being REPLACED —
  # create time is the only cheap opportunity, and the old default threw it away. Encryption
  # posture should not be a side effect of a DR decision, so the DR gate is gone and
  # rds_create_cmk with it.
  #
  # EXISTING stacks whose database is already on the AWS-managed key must pin
  # rds_adopt_dr_cmk = false (or pin rds_kms_key_id to their adopted CMK) to keep the same key
  # ARN — otherwise this flips and the KMS-key change replaces the DB. The db-replace-guard
  # PLAN policy blocks that unless the stack carries the allow-db-replace label, so the
  # aggressive default fails closed.
  # An RDS INSTANCE restored from a snapshot is excluded, because RDS will not honour the key.
  # RestoreDBClusterFromSnapshot (Aurora) takes a KmsKeyId and re-encrypts, but
  # RestoreDBInstanceFromDBSnapshot has no storage-key parameter at all — the restored instance
  # keeps the snapshot's key, and re-encrypting means copy-snapshot under the new key FIRST and
  # then restoring. So creating a CMK here would not converge: the instance comes up on the
  # snapshot's key while kms_key_id says otherwise, and because the key is immutable every
  # later plan demands another replacement. Such a stack must pin rds_kms_key_id to the
  # snapshot's actual key instead. Aurora is deliberately NOT excluded — it re-encrypts fine,
  # which is what makes the fleet's snapshot-restore key swap possible at all.
  rds_snapshot_restored_instance = var.db_engine == "rds" && var.rds_snapshot_identifier != ""

  rds_use_tf_cmk  = var.rds_adopt_dr_cmk && var.rds_kms_key_id == "alias/aws/rds" && !local.rds_snapshot_restored_instance
  rds_kms_key_arn = local.rds_use_tf_cmk ? aws_kms_key.rds_cmk[0].arn : data.aws_kms_key.rds.arn

  # Aurora gets its own key selection, WITHOUT the snapshot exclusion above, and the split
  # matters more than it looks. The exclusion is about a limitation of
  # RestoreDBInstanceFromDBSnapshot; Aurora does not share it. But db_engine stays "rds" for
  # the whole DMS migration by design (the app is still served by the RDS primary until the
  # cutover apply), so a stack mid-migration satisfies rds_snapshot_restored_instance while
  # aurora.tf is creating a brand-new EMPTY cluster that DMS will load. Feeding that cluster
  # rds_kms_key_arn denied it a CMK for a reason that has nothing to do with it, and since the
  # key is immutable at create the only way back is a snapshot-restore swap later.
  #
  # That is not hypothetical: it is why all four migrated 3M clusters sit on the AWS-managed
  # key. Reading rds_adopt_dr_cmk = false on those envs suggested they had opted out, but this
  # local would have overridden them to the managed key even set true.
  #
  # Safe for a snapshot-restored Aurora too, which is the case the exclusion was written for:
  # RestoreDBClusterFromSnapshot accepts KmsKeyId and re-encrypts, and that asymmetry is
  # exactly what the fleet's key-swap runbook depends on.
  # aurora_present is in the condition so the key is created only when there is actually an
  # Aurora cluster to encrypt. Without it, any stack with rds_adopt_dr_cmk = true but no Aurora
  # (qa today) would mint a CMK on its next apply that nothing consumes, and it also keeps the
  # aws_kms_key.rds_cmk[0] reference below safe when the count is zero.
  aurora_use_tf_cmk  = var.rds_adopt_dr_cmk && var.rds_kms_key_id == "alias/aws/rds" && local.aurora_present
  aurora_kms_key_arn = local.aurora_use_tf_cmk ? aws_kms_key.rds_cmk[0].arn : data.aws_kms_key.rds.arn

  # RDS automated-backup cross-region replication is only possible when the DB
  # uses a customer-managed key (either our created CMK or an operator-pinned one).
  # Aurora DR uses Global Database, not automated-backup replication.
  #
  # Gated on db_uses_aurora, NOT db_engine: mid-migration db_engine is still "rds" while the
  # live database is already Aurora. Keying off db_engine there would point the replication at
  # the fenced RDS source, cross-region-replicating backups of a retired database while the
  # live Aurora had none.
  dr_rds_enabled = !local.db_uses_aurora && var.enable_dr && (local.rds_use_tf_cmk || data.aws_kms_key.rds.key_manager == "CUSTOMER")
}

# Defense-in-depth guardrail. The real selection + blocklist enforcement happens
# in the admin layer; this only catches a missing/echoed injection.
check "dr_region_valid" {
  assert {
    condition     = !var.enable_dr || (var.dr_region != "" && var.dr_region != data.aws_region.current.region)
    error_message = "enable_dr is true but dr_region is empty or equals the primary region (${data.aws_region.current.region}). The admin layer must inject TG_AWS_DR_REGION, or set dr_region explicitly."
  }
}

# The snapshot-restore exclusion above is silent otherwise: the stack falls back to
# data.aws_kms_key.rds, which is alias/aws/rds unless pinned, and that is very likely NOT the
# snapshot's key. Nothing breaks (RDS ignores the key on restore), but the operator should know
# the config does not describe reality and that DR needs an explicit pin.
check "rds_snapshot_key_pinned" {
  assert {
    condition     = !local.rds_snapshot_restored_instance || var.rds_kms_key_id != "alias/aws/rds"
    error_message = "db_engine is \"rds\" and rds_snapshot_identifier is set, but rds_kms_key_id is unpinned. RestoreDBInstanceFromDBSnapshot cannot choose a storage key, so the instance will come up on the SNAPSHOT's key regardless of this config, and no customer-managed key is created (a created CMK would never converge). Pin rds_kms_key_id to the snapshot's actual key so the config matches reality — and so DR can tell whether that key is customer-managed. To move such a database onto a new CMK, copy the snapshot under the new key first, then restore from the copy."
  }
}

# Non-blocking warning surfaced on every plan/apply when DR is on but the primary
# database's cross-region backup replication is NOT active. The reason differs by
# engine, so the message is engine-aware: RDS replicates its automated backups only
# under a customer-managed key; Aurora has no automated-backup replication at all and
# uses Global Database instead. S3 content replicates either way.
#
# Three branches, all selected by db_uses_aurora (the LIVE engine) rather than db_is_aurora
# (the configured one). Mid-migration those disagree — db_engine stays "rds" while the app is
# already on Aurora — and the old two-branch form then explained an Aurora database with the
# RDS text, telling the operator to adopt a CMK on a database that was not the live one.
#
# The migration branch is db_uses_aurora && !db_is_aurora, i.e. cutover/cleanup only. NOT
# aurora_migration_active, which is also true at "provision" — there the Aurora cluster exists
# but is DMS-only and the live database is still the RDS instance, so that stack wants the RDS
# CMK advice and gets it by falling through to the third branch.
#
# The steady-state Aurora branch is currently DEAD, and deliberately so: aurora_dr_enabled is
# db_is_aurora && enable_dr && aurora_dr_partition_ok, and that last term is a hardcoded true
# (dr_aurora.tf), so with db_engine = "aurora" the assert can never fail. Keep the branch —
# it becomes live the moment aurora_dr_partition_ok turns into a real check, which is exactly
# the case where an operator would need that text.
check "dr_rds_replicable" {
  assert {
    condition = !var.enable_dr || local.dr_rds_enabled || local.aurora_dr_enabled
    error_message = local.db_uses_aurora && !local.db_is_aurora ? (
      "DR is enabled and S3 content replicates cross-region, but the database does NOT while an Aurora migration is in flight: the live database is the Aurora cluster, and Aurora DR (Global Database) only attaches once the env finishes the migration with db_engine = \"aurora\". Expected during a migration."
      ) : local.db_uses_aurora ? (
      "DR is enabled and S3 content replicates cross-region. The Aurora database replicates only when the global-database secondary is configured — ensure enable_dr is true and the admin layer injected a same-partition dr_region. (On GovCloud, confirm the engine version supports Global Database.)"
      ) : (
      "DR is enabled and S3 content replicates cross-region, but the RDS database does NOT: it is encrypted with an AWS-managed KMS key, so its automated backups are ineligible for cross-region replication. Adopt a customer-managed key — rds_adopt_dr_cmk = true (the default) for a fresh DB, or pin rds_kms_key_id to an existing CMK (a key change replaces the DB). See the DR cold-recovery runbook."
    )
  }
}

# Customer-managed key for the primary database — the RDS instance or the Aurora cluster
# (aurora.tf reads local.rds_kms_key_arn too), and the BI database alongside them. Created by
# DEFAULT now; only rds_adopt_dr_cmk = false or an explicit rds_kms_key_id opts out, which is
# how databases already living on the AWS-managed key stay put. CMK encryption is worth having
# on its own, and it is the prerequisite for RDS automated-backup cross-region replication and
# for encrypting an Aurora global secondary in the DR region.
resource "aws_kms_key" "rds_cmk" {
  # Either path can be the one that wants it: a fresh Aurora cluster mid-migration needs the
  # key even when the RDS snapshot exclusion is holding rds_use_tf_cmk false.
  count = local.rds_use_tf_cmk || local.aurora_use_tf_cmk ? 1 : 0

  description             = "${local.identifier} database encryption (customer-managed; DR-replicable)"
  enable_key_rotation     = true
  deletion_window_in_days = var.protect_resources ? 30 : 7
  tags                    = local.tags
}

resource "aws_kms_alias" "rds_cmk" {
  count = local.rds_use_tf_cmk ? 1 : 0

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

  # Mirror the SOURCE buckets (s3.tf aws_s3_bucket.guide_buckets), which have always been
  # force_destroy = !protect_resources. This resource omitted it entirely and so defaulted
  # to false on EVERY stack, protected or not.
  #
  # That made a DR-enabled stack undestroyable the moment replication put anything in the
  # destination - which is its whole job. Terraform empties the source bucket, then dies on
  # the replica:
  #
  #   deleting S3 Bucket (…-image-dr-…): api error BucketNotEmpty
  #
  # leaving the DR buckets, and everything the failed destroy had not reached yet,
  # stranded. Hit twice by ephemeral harness stacks (protect_resources = false) whose
  # teardown then had to be finished by hand.
  #
  # protect_resources = true is unaffected: those keep force_destroy = false, so a real
  # deploy's DR data still cannot be deleted out from under it.
  force_destroy = !var.protect_resources

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

# Same multipart hygiene as the primary buckets (s3.tf), plus two replica-only
# rules. Noncurrent versions EXPIRE here (30d) instead of archiving: the
# indefinite undo history already lives in the primary's Deep Archive tier, and
# without a rule the replica accumulates every superseded version forever.
# Current objects go to Intelligent-Tiering on day 0: a replica is write-only
# until a disaster, IT keeps millisecond access with no retrieval fee (so RTO
# is unaffected, unlike IA or the archive tiers), and objects under 128K are
# exempt from the monitoring fee so small images cannot turn it into a loss.
resource "aws_s3_bucket_lifecycle_configuration" "dr_guide_buckets" {
  for_each = aws_s3_bucket.dr_guide_buckets
  provider = aws.dr

  bucket = each.value.id

  rule {
    id     = "finops-hygiene"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }

    transition {
      days          = 0
      storage_class = "INTELLIGENT_TIERING"
    }

    noncurrent_version_expiration {
      noncurrent_days = 30
    }
  }
}

# S3.5: reject plaintext requests. Replication and restore tooling are TLS-only,
# so the deny has no legitimate traffic to break.
resource "aws_s3_bucket_policy" "dr_guide_buckets" {
  for_each = aws_s3_bucket.dr_guide_buckets
  provider = aws.dr

  bucket = each.value.id
  policy = data.aws_iam_policy_document.dr_guide_buckets_ssl_only[each.key].json
}

data "aws_iam_policy_document" "dr_guide_buckets_ssl_only" {
  for_each = aws_s3_bucket.dr_guide_buckets

  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]

    resources = [
      each.value.arn,
      "${each.value.arn}/*",
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

# Replica of the primary pdf bucket's ACL carve-out (s3.tf): replicating an
# object that carries an ACL needs the destination to accept ACLs too. Same
# removal condition as the primary. Public access stays blocked here as well.
resource "aws_s3_bucket_ownership_controls" "dr_guide_pdf_bucket" {
  for_each = contains(keys(aws_s3_bucket.dr_guide_buckets), "pdf") ? { pdf = aws_s3_bucket.dr_guide_buckets["pdf"] } : {}
  provider = aws.dr

  bucket = each.value.id

  rule {
    object_ownership = "ObjectWriter"
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
  name               = "${local.identifier}-${data.aws_region.current.region}-dr-s3-replication"
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
  name   = "${local.identifier}-${data.aws_region.current.region}-dr-s3-replication"
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
