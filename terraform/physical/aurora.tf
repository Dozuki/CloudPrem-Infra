# Aurora MySQL 8.4 Serverless v2 cluster (active when var.db_engine == "aurora").
# Produces the same connection facts as the RDS path via local.db (db.tf).

# No generated password when cloning: a copy-on-write clone inherits the source
# cluster's master credentials (supplied to the app via var.aurora_clone_master_password).
resource "random_password" "aurora" {
  count   = local.db_is_aurora && !local.db_is_clone ? 1 : 0
  length  = 40
  special = false
}

# Guards for the clone path (only evaluated when cloning).
resource "terraform_data" "aurora_clone_guard" {
  count = local.db_is_clone ? 1 : 0
  lifecycle {
    precondition {
      condition     = var.aurora_snapshot_identifier == ""
      error_message = "aurora_clone_source_cluster_id and aurora_snapshot_identifier are mutually exclusive — a cluster can restore from a snapshot OR clone a source, not both."
    }
    precondition {
      condition     = var.aurora_clone_master_password != ""
      error_message = "aurora_clone_master_password is required when aurora_clone_source_cluster_id is set: the clone inherits the source's master password, so the app's stored credentials must match it."
    }
  }
}

module "aurora" {
  source  = "terraform-aws-modules/rds-aurora/aws"
  version = "10.2.0"

  count = local.db_is_aurora ? 1 : 0

  name        = local.identifier
  engine      = "aurora-mysql"
  engine_mode = "provisioned"
  # On a clone, master_username/password and KMS key are inherited from the source
  # cluster — setting them on a copy-on-write restore is rejected by AWS, so null them.
  engine_version  = var.aurora_engine_version
  master_username = local.db_is_clone ? null : local.db_username

  manage_master_user_password = false
  master_password_wo          = one(random_password.aurora[*].result)
  master_password_wo_version  = local.db_is_clone ? null : 1

  serverlessv2_scaling_configuration = {
    min_capacity = var.aurora_min_acu
    max_capacity = var.aurora_max_acu
  }
  instances = merge(
    { writer = { instance_class = "db.serverless", performance_insights_enabled = true } },
    var.rds_multi_az ? { reader = { instance_class = "db.serverless", performance_insights_enabled = true } } : {}
  )

  vpc_security_group_ids = [module.primary_database_sg.security_group_id]
  subnets                = local.private_subnet_ids
  create_db_subnet_group = true

  # Use primary_database_sg (in the app VPC, with the MySQL-from-EKS ingress) as the
  # cluster's only security group. The rds-aurora module otherwise creates its own SG
  # and, with no vpc_id passed, places it in the account's DEFAULT VPC — which fails
  # CreateDBCluster ("DB instance and EC2 security group are in different VPCs"). The
  # RDS path doesn't create its own SG, so only the aurora path needs this.
  create_security_group = false

  storage_encrypted = true
  kms_key_id        = local.db_is_clone ? null : local.rds_kms_key_arn

  # Restore-from-snapshot and clone are mutually exclusive (guarded above).
  snapshot_identifier = local.db_is_clone ? null : (var.aurora_snapshot_identifier != "" ? var.aurora_snapshot_identifier : null)

  # Copy-on-write clone of a pre-seeded "golden" cluster — fast ephemeral DBs for tests.
  restore_to_point_in_time = local.db_is_clone ? {
    source_cluster_identifier  = var.aurora_clone_source_cluster_id
    restore_type               = "copy-on-write"
    use_latest_restorable_time = true
  } : null

  cluster_parameter_group = {
    family = "aurora-mysql8.4"
    parameters = [
      { name = "binlog_format", value = "ROW", apply_method = "pending-reboot" },
      { name = "binlog_row_image", value = "full", apply_method = "pending-reboot" },
      { name = "binlog_checksum", value = "NONE", apply_method = "pending-reboot" },
    ]
  }

  db_parameter_group = {
    family = "aurora-mysql8.4"
    parameters = [
      { name = "group_concat_max_len", value = "33554432", apply_method = "pending-reboot" },
    ]
  }

  apply_immediately            = !var.protect_resources
  deletion_protection          = var.protect_resources
  skip_final_snapshot          = !var.protect_resources
  copy_tags_to_snapshot        = true
  backup_retention_period      = var.rds_backup_retention_period
  preferred_backup_window      = "17:00-19:00"
  preferred_maintenance_window = "sun:19:00-sun:23:00"

  tags = local.tags
}
