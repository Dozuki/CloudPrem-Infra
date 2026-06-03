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
