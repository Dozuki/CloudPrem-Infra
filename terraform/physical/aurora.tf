# Aurora MySQL 8.0 Serverless v2 cluster (active when var.db_engine == "aurora").
# Produces the same connection facts as the RDS path via local.db (db.tf).

resource "random_password" "aurora" {
  count   = local.db_is_aurora ? 1 : 0
  length  = 40
  special = false
}

module "aurora" {
  source  = "terraform-aws-modules/rds-aurora/aws"
  version = "10.2.0"

  count = local.db_is_aurora ? 1 : 0

  name            = local.identifier
  engine          = "aurora-mysql"
  engine_mode     = "provisioned"
  engine_version  = var.aurora_engine_version
  master_username = local.db_username

  manage_master_user_password = false
  master_password_wo          = random_password.aurora[0].result
  master_password_wo_version  = 1

  serverlessv2_scaling_configuration = {
    min_capacity = var.aurora_min_acu
    max_capacity = var.aurora_max_acu
  }
  instances = merge(
    { writer = { instance_class = "db.serverless", performance_insights_enabled = true } },
    var.rds_multi_az ? { reader = { instance_class = "db.serverless", performance_insights_enabled = true } } : {}
  )

  # We supply the shared primary_database_sg; don't let the module create its own SG
  # (it has no vpc_id and would fall back to a default VPC, which doesn't exist here).
  create_security_group  = false
  vpc_security_group_ids = [module.primary_database_sg.security_group_id]
  subnets                = local.private_subnet_ids
  create_db_subnet_group = true

  storage_encrypted = true
  kms_key_id        = local.rds_kms_key_arn

  snapshot_identifier = var.aurora_snapshot_identifier != "" ? var.aurora_snapshot_identifier : null

  cluster_parameter_group = {
    family = "aurora-mysql8.0"
    parameters = [
      { name = "binlog_format", value = "ROW", apply_method = "pending-reboot" },
      { name = "binlog_row_image", value = "full", apply_method = "pending-reboot" },
      { name = "binlog_checksum", value = "NONE", apply_method = "pending-reboot" },
    ]
  }

  db_parameter_group = {
    family = "aurora-mysql8.0"
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
