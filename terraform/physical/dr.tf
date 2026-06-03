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
}

# Defense-in-depth guardrail. The real selection + blocklist enforcement happens
# in the admin layer; this only catches a missing/echoed injection.
check "dr_region_valid" {
  assert {
    condition     = !var.enable_dr || (var.dr_region != "" && var.dr_region != data.aws_region.current.id)
    error_message = "enable_dr is true but dr_region is empty or equals the primary region (${data.aws_region.current.id}). The admin layer must inject TG_AWS_DR_REGION, or set dr_region explicitly."
  }
}

# DR-region CMK for replicated RDS automated backups (the encrypted source DB
# requires a destination-region key).
resource "aws_kms_key" "dr_rds" {
  count    = local.dr_enabled ? 1 : 0
  provider = aws.dr

  description         = "${local.identifier} DR replicated RDS backups"
  enable_key_rotation = true
  tags                = local.tags
}

resource "aws_kms_alias" "dr_rds" {
  count    = local.dr_enabled ? 1 : 0
  provider = aws.dr

  name_prefix   = "alias/${local.identifier}/dr/rds/"
  target_key_id = aws_kms_key.dr_rds[0].id
}

# DR-region CMK for the destination S3 buckets.
resource "aws_kms_key" "dr_s3" {
  count    = local.dr_enabled ? 1 : 0
  provider = aws.dr

  description         = "${local.identifier} DR replicated S3 content"
  enable_key_rotation = true
  tags                = local.tags
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
  count    = local.dr_enabled ? 1 : 0
  provider = aws.dr

  source_db_instance_arn = local.dr_source_db_arn
  kms_key_id             = aws_kms_key.dr_rds[0].arn

  depends_on = [module.primary_database]
}
