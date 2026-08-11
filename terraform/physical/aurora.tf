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

# Suffix for the Aurora final-snapshot names (this file and bi.tf). The rds-aurora
# module has no final_snapshot_identifier_PREFIX variable the way the rds module
# does, only the literal identifier, so the uniqueness the rds path gets for free
# has to be built here. Same shape as what terraform-aws-modules/rds appends.
#
# A destroy does not consume the value, so a stack that is destroyed, recreated
# and destroyed again reuses this hex and the second delete fails
# DBSnapshotAlreadyExists. That matches the existing rds and BI paths rather than
# regressing them; rename or drop the stale snapshot when it happens.
resource "random_id" "aurora_final_snapshot" {
  count       = local.aurora_present || local.bi_uses_aurora ? 1 : 0
  byte_length = 4
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

  # Managed read-replica seed (RDS -> Aurora). Null on every normal stack, so this pair
  # is a no-op unless an env opts in. When set, the module sends database_name and
  # master_username as null and suppresses master_password_wo below - the replica takes
  # the source instance's credentials. See the variable for why the pin is permanent.
  replication_source_identifier = var.aurora_replication_source_identifier
  is_primary_cluster            = var.aurora_replication_source_identifier == null

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
  kms_key_id        = local.aurora_kms_key_arn

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
        # lower_case_table_names is intentionally NOT set here even though the
        # migration requires 0 (it must match the RDS source). 0 IS the engine
        # default, and AWS refuses ModifyDBClusterParameterGroup on that
        # parameter once the PG is associated with a live cluster - so an
        # explicit entry bricks every EXISTING aurora env's first apply of this
        # code (caught on dev-min). The migration runner asserts the effective
        # value at its gates instead.
      ],
      # Migration-only relaxation, removed again after cleanup. The event
      # scheduler stays OFF until the cutover repoint so nothing fires on the
      # target before the first app write (the migration runner asserts it at
      # its gates). local_infile, which the DMS full load needs, is NOT pinned
      # here: same class of landmine as lower_case_table_names above, different
      # failure mode. That one AWS rejects outright; this one AWS accepts and
      # drops, because local_infile is an instance-level parameter this
      # cluster-level API merely tolerates and 1 is already the engine default.
      # The write is a silent no-op AWS keeps reporting as
      # Source=system/ApplyMethod=pending-reboot, so the entry drifts forever
      # (terraform-provider-aws#30802). The runner asserts the effective value
      # at its preload gate instead.
      local.aurora_migration_active && var.aurora_migration_state == "provision" ? [
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

  apply_immediately   = local.db_apply_immediately
  deletion_protection = var.protect_resources
  skip_final_snapshot = !var.protect_resources
  # Required whenever skip_final_snapshot is false. Without it the provider
  # refuses the delete outright ("FinalSnapshotIdentifier is required when a
  # final snapshot is required"), which made the aurora_migration_state = "off"
  # abort path impossible to run through terraform. Clearing deletion protection
  # out of band gets past the first gate only to hit this one, and
  # skip_final_snapshot is a terraform-only attribute with no RDS API equivalent,
  # so there is nothing to override by hand. The cluster then has to be
  # snapshotted and deleted manually, which is how this was found.
  final_snapshot_identifier    = "${local.identifier}-final-${random_id.aurora_final_snapshot[0].hex}"
  copy_tags_to_snapshot        = true
  backup_retention_period      = var.rds_backup_retention_period
  preferred_backup_window      = "17:00-19:00"
  preferred_maintenance_window = "sun:19:00-sun:23:00"

  tags = local.tags
}
