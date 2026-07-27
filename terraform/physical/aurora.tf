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
  count   = local.aurora_present ? 1 : 0
  length  = 40
  special = false
}

module "aurora" {
  source  = "terraform-aws-modules/rds-aurora/aws"
  version = "10.2.0"

  # Present for the steady-state aurora engine AND during an RDS->Aurora DMS
  # migration (aurora-migration.tf), where it runs ALONGSIDE the live RDS. Both
  # gates keep the same module instance alive, so the eventual db_engine="aurora"
  # + aurora_migration_state="off" flip is a no-op for this cluster.
  count = local.aurora_present ? 1 : 0

  name            = local.identifier
  engine          = "aurora-mysql"
  engine_mode     = "provisioned"
  engine_version  = var.aurora_engine_version
  master_username = local.db_username

  # Permit the in-place major-version upgrade path (Aurora MySQL v3/8.0 to v4/8.4):
  # AWS rejects a ModifyDBCluster engine_version bump across majors without it. Only
  # permits the upgrade; the engine_version change is what triggers one. Matches the
  # rds and bi paths, which already set this.
  allow_major_version_upgrade = true

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

  # Network guard during a migration: while state="provision" the cluster is
  # reachable ONLY by the migration DMS instance + the bastion (guard SG,
  # aurora-migration.tf) - the app cannot touch the empty/loading target even by
  # misconfiguration (the fresh-install sharp edge). At "cutover" this swaps
  # in-place to the primary DB SG and the app path opens.
  vpc_security_group_ids = (
    var.aurora_migration_state == "provision"
    ? [module.aurora_migration_guard_sg[0].security_group_id]
    : [module.primary_database_sg.security_group_id]
  )
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

  # Log exports are default-on (log-and-audit-everything). The module creates
  # the /aws/rds/cluster/... log groups itself so they carry retention instead
  # of landing as auto-created never-expire groups. The parameter-group entries
  # below make the exported logs actually exist: exports only ship what the
  # engine writes.
  enabled_cloudwatch_logs_exports        = var.rds_log_exports
  create_cloudwatch_log_group            = true
  cloudwatch_log_group_retention_in_days = 365

  cluster_parameter_group = {
    family = local.aurora_param_family
    parameters = concat(
      [
        { name = "binlog_format", value = "ROW", apply_method = "pending-reboot" },
        { name = "binlog_row_image", value = "full", apply_method = "pending-reboot" },
        { name = "binlog_checksum", value = "NONE", apply_method = "pending-reboot" },
        # Explicit even though 0 is the engine default: it must match the RDS
        # source at migration time and is immutable after cluster creation.
        { name = "lower_case_table_names", value = "0", apply_method = "pending-reboot" },
      ],
      # Migration-only relaxations, removed again after cleanup. local_infile is
      # required by DMS full load; the event scheduler stays OFF until the
      # cutover repoint so nothing fires on the target before the first app
      # write (the migration runner asserts both at its gates).
      local.aurora_migration_active && var.aurora_migration_state == "provision" ? [
        { name = "local_infile", value = "1", apply_method = "immediate" },
        { name = "event_scheduler", value = "OFF", apply_method = "immediate" },
      ] : [],
      contains(var.rds_log_exports, "audit") ? [
        { name = "server_audit_logging", value = "1", apply_method = "immediate" },
        { name = "server_audit_events", value = "CONNECT,QUERY_DCL,QUERY_DDL", apply_method = "immediate" },
      ] : [],
      contains(var.rds_log_exports, "slowquery") ? [
        { name = "slow_query_log", value = "1", apply_method = "immediate" },
      ] : [],
      contains(var.rds_log_exports, "general") ? [
        { name = "general_log", value = "1", apply_method = "immediate" },
      ] : [],
    )
  }

  db_parameter_group = {
    family = local.aurora_param_family
    parameters = [
      { name = "group_concat_max_len", value = "33554432", apply_method = "pending-reboot" },
    ]
  }

  apply_immediately            = local.db_apply_immediately
  deletion_protection          = var.protect_resources
  skip_final_snapshot          = !var.protect_resources
  copy_tags_to_snapshot        = true
  backup_retention_period      = var.rds_backup_retention_period
  preferred_backup_window      = "17:00-19:00"
  preferred_maintenance_window = "sun:19:00-sun:23:00"

  tags = local.tags
}
