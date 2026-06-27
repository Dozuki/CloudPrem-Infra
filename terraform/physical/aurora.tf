# Aurora MySQL Serverless v2 cluster (active when var.db_engine == "aurora").
# Produces the same connection facts as the RDS path via local.db (db.tf).

locals {
  # Parameter-group family must match the engine's major.minor (aurora-mysql8.0
  # vs aurora-mysql8.4), or RestoreDBClusterFromSnapshot rejects the create with
  # "DBParameterGroupFamily ... cannot be used for this instance". Derive it from
  # aurora_engine_version ("8.0.mysql_aurora.3.12.0" -> "8.0") instead of pinning
  # 8.4, so an 8.0 snapshot restore (e.g. gov MPC migrations) gets the right family.
  aurora_param_family = "aurora-mysql${split(".mysql_aurora", var.aurora_engine_version)[0]}"
}

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

  vpc_security_group_ids = [module.primary_database_sg.security_group_id]
  subnets                = local.private_subnet_ids
  create_db_subnet_group = true

  # Use primary_database_sg (in the app VPC, with the MySQL-from-EKS ingress) as the
  # cluster's only security group. The rds-aurora module otherwise creates its own SG
  # and, with no vpc_id passed, places it in the account's DEFAULT VPC, which fails
  # CreateDBCluster ("DB instance and EC2 security group are in different VPCs"). The
  # RDS path doesn't create its own SG, so only the aurora path needs this.
  create_security_group = false

  storage_encrypted = true
  kms_key_id        = local.rds_kms_key_arn

  snapshot_identifier = var.aurora_snapshot_identifier != "" ? var.aurora_snapshot_identifier : null

  cluster_parameter_group = {
    family = local.aurora_param_family
    parameters = [
      { name = "binlog_format", value = "ROW", apply_method = "pending-reboot" },
      { name = "binlog_row_image", value = "full", apply_method = "pending-reboot" },
      { name = "binlog_checksum", value = "NONE", apply_method = "pending-reboot" },
    ]
  }

  db_parameter_group = {
    family = local.aurora_param_family
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
