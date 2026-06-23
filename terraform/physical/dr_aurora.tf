# Aurora cross-region DR (DR Phase 2) — headless Aurora Global Database secondary in
# var.dr_region. Gated on enable_dr (like the rest of dr.tf) and only for the aurora
# engine. The CMK on the primary (dr.tf rds_cmk, used by aurora.tf) is the prerequisite.
#
# Adoption is NON-DESTRUCTIVE: aws_rds_global_cluster adopts the EXISTING primary cluster
# in place via source_db_cluster_identifier. The rds-aurora module's aws_rds_cluster
# already ignore_changes on global_cluster_identifier, so AWS stamping the global
# membership onto the primary neither drifts nor replaces it. The db-replace-guard PLAN
# policy is the backstop if that ever regresses.
#
# Partition-aware: aurora_dr_partition_ok stays true — Global Database is available for
# the pinned 8.4 engine in BOTH the aws and aws-us-gov partitions (gov DR is gov-west <->
# gov-east; verified 2026-06-23, see the DR Phase 2 spec). Flip to a partition check only
# if a future engine/region loses Global Database in gov.
locals {
  aurora_dr_partition_ok = true
  aurora_dr_enabled      = local.db_is_aurora && var.enable_dr && local.aurora_dr_partition_ok
}

# DR-region AZs (aws.dr is the Terragrunt-generated DR provider).
data "aws_availability_zones" "dr" {
  count    = local.aurora_dr_enabled ? 1 : 0
  provider = aws.dr
  state    = "available"
}

# --- DR-region networking ----------------------------------------------------
# A secondary Aurora cluster is VPC-bound even when headless, so it needs a DB
# subnet group in the DR region. Minimal footprint: private subnets only, no NAT/IGW
# (the headless cluster needs no egress; failover instances live in these subnets).
resource "aws_vpc" "dr_aurora" {
  count                = local.aurora_dr_enabled ? 1 : 0
  provider             = aws.dr
  cidr_block           = var.dr_vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = merge(local.tags, { Name = "${local.identifier}-dr-aurora" })
}

# One private /24 per AZ (up to 3), carved from dr_vpc_cidr.
resource "aws_subnet" "dr_aurora" {
  for_each          = local.aurora_dr_enabled ? toset(slice(data.aws_availability_zones.dr[0].names, 0, min(3, length(data.aws_availability_zones.dr[0].names)))) : []
  provider          = aws.dr
  vpc_id            = aws_vpc.dr_aurora[0].id
  availability_zone = each.value
  cidr_block        = cidrsubnet(var.dr_vpc_cidr, 4, index(data.aws_availability_zones.dr[0].names, each.value))
  tags              = merge(local.tags, { Name = "${local.identifier}-dr-aurora-${each.value}" })
}

resource "aws_db_subnet_group" "dr_aurora" {
  count       = local.aurora_dr_enabled ? 1 : 0
  provider    = aws.dr
  name_prefix = "${local.identifier}-dr-aurora-"
  subnet_ids  = [for s in aws_subnet.dr_aurora : s.id]
  tags        = local.tags
}

resource "aws_security_group" "dr_aurora" {
  count       = local.aurora_dr_enabled ? 1 : 0
  provider    = aws.dr
  name_prefix = "${local.identifier}-dr-aurora-"
  description = "DR-region Aurora global secondary (headless; ingress added at failover)"
  vpc_id      = aws_vpc.dr_aurora[0].id
  tags        = local.tags

  # No ingress while headless. On failover the runbook adds ingress for the promoted
  # cluster from the app's DR-region SG. Egress is intra-VPC only (no IGW).
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [var.dr_vpc_cidr]
  }
}

# --- DR-region CMK for the secondary's storage (mirrors aws_kms_key.dr_rds) ----
resource "aws_kms_key" "dr_aurora" {
  count                   = local.aurora_dr_enabled ? 1 : 0
  provider                = aws.dr
  description             = "${local.identifier} DR Aurora global secondary storage"
  enable_key_rotation     = true
  deletion_window_in_days = var.protect_resources ? 30 : 7
  tags                    = local.tags
}

resource "aws_kms_alias" "dr_aurora" {
  count         = local.aurora_dr_enabled ? 1 : 0
  provider      = aws.dr
  name_prefix   = "alias/${local.identifier}/dr/aurora/"
  target_key_id = aws_kms_key.dr_aurora[0].id
}

# --- Global cluster: adopt the existing primary in place ----------------------
resource "aws_rds_global_cluster" "aurora" {
  count                        = local.aurora_dr_enabled ? 1 : 0
  global_cluster_identifier    = "${local.identifier}-global"
  engine                       = "aurora-mysql"
  engine_version               = var.aurora_engine_version
  source_db_cluster_identifier = module.aurora[0].cluster_arn
  force_destroy                = true

  lifecycle {
    # After adoption AWS stamps these onto the global cluster from the source cluster;
    # they are the adoption seed, not ongoing config. Avoid perpetual diffs / replace.
    ignore_changes = [source_db_cluster_identifier, engine_version]
  }
}

# --- Headless secondary cluster in the DR region (no instances) ---------------
resource "aws_rds_cluster" "dr_aurora_secondary" {
  count    = local.aurora_dr_enabled ? 1 : 0
  provider = aws.dr

  cluster_identifier        = "${local.identifier}-dr"
  engine                    = "aurora-mysql"
  engine_version            = var.aurora_engine_version
  global_cluster_identifier = aws_rds_global_cluster.aurora[0].id
  storage_encrypted         = true
  kms_key_id                = aws_kms_key.dr_aurora[0].arn
  # Required for an ENCRYPTED cross-region global secondary: the provider builds a
  # presigned URL to the primary region from source_region. Without it CreateDBCluster
  # fails at apply (not caught by validate or a plan-preview).
  source_region          = data.aws_region.current.region
  db_subnet_group_name   = aws_db_subnet_group.dr_aurora[0].name
  vpc_security_group_ids = [aws_security_group.dr_aurora[0].id]

  # master_username / master_password / database_name are inherited from the global
  # primary and must NOT be set on a secondary.
  skip_final_snapshot = !var.protect_resources
  apply_immediately   = !var.protect_resources

  # NO aws_rds_cluster_instance => headless: storage replication only, no compute cost.
  # Failover provisions an instance (see the Aurora failover runbook).

  lifecycle {
    ignore_changes = [replication_source_identifier]
  }

  depends_on = [aws_rds_global_cluster.aurora]
}

# --- Outputs (consumed by the failover runbook) -------------------------------
output "aurora_dr_global_cluster_id" {
  description = "Aurora global cluster id (empty unless the Aurora DR secondary is enabled)."
  value       = try(aws_rds_global_cluster.aurora[0].id, "")
}

output "aurora_dr_secondary_endpoint" {
  description = "Reader endpoint of the headless DR secondary (populated once instances exist post-failover)."
  value       = try(aws_rds_cluster.dr_aurora_secondary[0].reader_endpoint, "")
}
