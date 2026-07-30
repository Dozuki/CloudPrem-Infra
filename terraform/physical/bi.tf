
data "aws_iam_role" "dms-vpc-role" {
  count = (local.dms_enabled || local.aurora_migration_dms) ? try(length(data.aws_iam_roles.dms-vpc-roles.arns), 0) > 0 ? 1 : 0 : 0

  name = "dms-vpc-role"
}
data "aws_iam_roles" "dms-vpc-roles" {
  name_regex = "dms-vpc-role"
}
data "aws_iam_role" "dms-cloudwatch-role" {
  count = (local.dms_enabled || local.aurora_migration_dms) ? try(length(data.aws_iam_roles.dms-cloudwatch-roles.arns), 0) > 0 ? 1 : 0 : 0

  name = "dms-cloudwatch-logs-role"
}
data "aws_iam_roles" "dms-cloudwatch-roles" {
  name_regex = "dms-cloudwatch-logs-role"
}
# We create the dms-vpc-role and dms-cloudwatch-logs-role using a null_resource to prevent the removal of the
# account-wide role should this stack be deleted. In other words, to keep the role out of the state.
resource "null_resource" "create_dms_vpc_role" {
  # Needed by BI DMS and the Aurora migration rig alike.
  count = (local.dms_enabled || local.aurora_migration_dms) ? length(data.aws_iam_role.dms-vpc-role) > 0 ? 0 : 1 : 0

  provisioner "local-exec" {
    command = <<-EOT
      aws iam create-role \
        --role-name dms-vpc-role \
        --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"dms.${data.aws_partition.current.dns_suffix}"},"Action":"sts:AssumeRole"}]}'
      aws iam attach-role-policy \
        --role-name dms-vpc-role \
        --policy-arn arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonDMSVPCManagementRole
    EOT
  }
}
resource "null_resource" "create_dms_cloudwatch_role" {
  # Needed by BI DMS and the Aurora migration rig alike.
  count = (local.dms_enabled || local.aurora_migration_dms) ? length(data.aws_iam_role.dms-cloudwatch-role) > 0 ? 0 : 1 : 0

  provisioner "local-exec" {
    command = <<-EOT
      aws iam create-role \
        --role-name dms-cloudwatch-logs-role \
        --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"dms.${data.aws_partition.current.dns_suffix}"},"Action":"sts:AssumeRole"}]}'
      aws iam attach-role-policy \
        --role-name dms-cloudwatch-logs-role \
        --policy-arn arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonDMSCloudWatchLogsRole
    EOT
  }
}

resource "aws_dms_replication_subnet_group" "this" {
  count = local.dms_enabled ? 1 : 0

  replication_subnet_group_id          = "${local.identifier}-replication"
  replication_subnet_group_description = "${local.identifier} replication subnet group"

  subnet_ids = local.private_subnet_ids

  tags = local.tags
}

resource "aws_kms_key" "bi" {
  count = var.enable_bi ? 1 : 0

  description             = "BI KMS key for replication credentials"
  enable_key_rotation     = true
  deletion_window_in_days = var.protect_resources ? 30 : 7

  tags = local.tags
}

# The provisioned replication instance that used to live here is gone; the serverless
# replication below replaces it AND the replication task. Measured on the 3M fleet before
# switching: these replications sit under 5% CPU for 92-100% of hours (usac excepted), so a
# provisioned instance was billing 24/7 for capacity that was idle almost all of it. The
# subnet group, KMS key, certificate and both endpoints are unchanged and shared.

resource "aws_dms_certificate" "this" {
  count = local.dms_enabled ? 1 : 0

  certificate_id  = "${local.identifier}-dms-certificate"
  certificate_pem = file(local.ca_cert_pem_file)

  tags = local.tags

}

resource "aws_dms_endpoint" "source" {
  count = local.dms_enabled ? 1 : 0

  endpoint_id                 = "${local.identifier}-source"
  certificate_arn             = aws_dms_certificate.this[0].certificate_arn
  ssl_mode                    = "verify-full"
  endpoint_type               = "source"
  engine_name                 = "mysql"
  extra_connection_attributes = "afterConnectScript=call mysql.rds_set_configuration('binlog retention hours', 24);"
  port                        = 3306
  kms_key_arn                 = aws_kms_key.bi[0].arn

  username    = local.db_username
  password    = local.db_password
  server_name = local.db_host

  tags = local.tags
}

resource "aws_dms_endpoint" "target" {
  count = local.dms_enabled ? 1 : 0

  endpoint_id                 = "${local.identifier}-target"
  certificate_arn             = aws_dms_certificate.this[0].certificate_arn
  ssl_mode                    = "verify-full"
  endpoint_type               = "target"
  engine_name                 = "mysql"
  extra_connection_attributes = "afterConnectScript=call mysql.rds_set_configuration('binlog retention hours', 24);Initstmt=SET FOREIGN_KEY_CHECKS=0;"
  port                        = 3306
  kms_key_arn                 = aws_kms_key.bi[0].arn

  # Engine-agnostic (main.tf): the BI target is a provisioned instance on the default path and an
  # Aurora Serverless cluster when bi_db_engine = "aurora". engine_name stays "mysql" either way
  # - DMS treats Aurora MySQL as a wire-compatible MySQL target. Changing any of these three
  # requires the task to be Stopped first; DMS rejects ModifyEndpoint on a running task.
  username    = local.bi_db_username
  password    = local.bi_db_password
  server_name = local.bi_db_host

  tags = local.tags
}

# DMS Serverless. One resource replaces the replication instance + replication task, and it
# autoscales between min and max DCU (1 DCU = 2 GB RAM) instead of billing a fixed instance.
#
# start_replication is FALSE, and the logical layer's dms-start Job keeps owning the start. It
# was set true here first, which reads better - the start becomes an attribute Terraform owns,
# and serverless runs its own Testing Connection phase, which was one of the two things that
# Job did. It also wedges a greenfield install, permanently:
#
#   The selection rules in static/dms_mapping.json match the `sites` and `%_guide` SCHEMAS.
#   Those are created by the logical layer's db-migrations Job, which runs after physical. On a
#   fresh stack this resource is therefore created against a source where nothing it selects
#   exists, DMS refuses initialization ("No tables were found at task initialization"), and the
#   provider - which waits for the replication to reach running - fails the physical apply. A
#   failed physical apply means logical never runs, so the schemas that would let the
#   replication start can never be created. Nothing recovers it on its own: the restart lambda
#   only fires on state-change events, and the 48h deprovision clock below starts ticking
#   meanwhile. Recoverable only by hand.
#
# Existing stacks never see this (their schemas exist), which is why the rehearsals passed. That
# is exactly the ordering dependency the Job was written for - its own comments say it waits for
# the app deployment to EXIST before starting anything - so the Job is kept and taught about
# serverless rather than deleted.
#
# The cost of false here is that a modify leaves the replication stopped: the provider's update
# path stops the replication before ModifyReplicationConfig and only restarts it when
# start_replication is true. Logical autodeploys after physical and the dms-start Job carries
# the dms_replication_generation output in its NAME, so the same modify that stops the
# replication also forces a fresh Job that starts it again; the exposure is the gap between
# the two applies, which matters only against the 48h deprovision clock. Do not leave a
# physical apply un-followed by a logical one.
#
# CAUTION on the rds -> aurora switch: it does NOT go through that path, and nothing automatic
# restarts the replication after it. The switch repoints aws_dms_endpoint.target at the new
# EMPTY Aurora cluster, and server_name/username/password are modify-in-place attributes, so
# the endpoint ARN never changes and this resource sees no diff at all. Two consequences. DMS
# rejects ModifyEndpoint while the replication runs, so the operator stops it first (the order
# is in the bi_db_engine variable description). And the generation hash is invariant across the
# switch, so no fresh dms-start Job appears - the operator owns the restart too, and it has to
# be reload-target. resume-processing would pick CDC up against an empty cluster and report
# Running with no historical rows at all. A "Running" status is NOT evidence the switch worked -
# verify TablesLoaded, not status.
#
# Endpoint attributes are deliberately kept OUT of the generation hash for that reason. A Job
# minted by an endpoint change would issue resume-processing, since it cannot tell a repoint
# from an ordinary stop, which is exactly the wrong call at a cutover.
#
# Sizing is deliberately NOT derived from the old instance class. The provisioned boxes were
# sized for peak full-load headroom and then sat idle; min_capacity_units is what matters for
# steady-state cost, and max_capacity_units only bills when a burst actually uses it. AWS's
# own guidance is to set max generously and let autoscaling decide.
#
# One sharp edge that has no provisioned equivalent: a serverless replication left stopped or
# failed for 48 HOURS is deprovisioned and can no longer be resumed - recovery is recreating
# this resource, not a start call. Coverage for that is deliberately NOT a second alarm: the
# state-change rule in monitoring.tf no longer filters on detail-type, so every serverless
# state change (stopped, failed, deprovisioned) already reaches Slack, and a dedicated rule
# would just double-alert the one event we least want buried in duplicates. Instead the
# restart lambda recognises the deprovisioned state and declines to attempt an impossible
# restart, so the alert stands on its own rather than being masked by a lambda error.
#
# Steady state needs no destroy-time stop provisioner: this resource's Delete calls
# stopReplication first, which no-ops when already stopped/created/failed and otherwise stops
# and waits, then deletes.
#
# The upgrade apply needs no manual pre-step, contrary to what these comments said at first.
# On an existing DMS-enabled stack the resource destroyed is the OLD
# aws_dms_replication_task, and the locked provider (v6.56.0) stops it for us:
# resourceReplicationTaskDelete calls stopReplicationTask, which no-ops when the task is not
# running and otherwise issues StopReplicationTask and WAITS for stopped, before deleting.
# dms-stop.sh and its null_resource were a workaround for provider issue #2083; the provider
# has since absorbed it, so removing them costs nothing.
#
# What a manual pre-stop does still buy is ordering. Terraform has no dependency edge between
# the old task's destroy and this config's create, so the serverless replication can begin its
# full load while the old task is still stopping - briefly two replications writing the same
# target. BI is rebuildable and the full load wins, so this is untidy rather than harmful, but
# pre-stopping with util/dms-stop.sh (kept as an optional operator tool) is how you get the
# clean sequence if you want it.
resource "aws_dms_replication_config" "this" {
  count = local.dms_enabled ? 1 : 0

  replication_config_identifier = local.identifier
  replication_type              = "full-load-and-cdc"
  source_endpoint_arn           = aws_dms_endpoint.source[0].endpoint_arn
  target_endpoint_arn           = aws_dms_endpoint.target[0].endpoint_arn
  table_mappings                = file("static/dms_mapping.json")
  replication_settings          = file("static/dms_config.json")
  # See the greenfield note above before flipping this to true.
  start_replication = false

  compute_config {
    replication_subnet_group_id = aws_dms_replication_subnet_group.this[0].id
    vpc_security_group_ids      = [module.bi_database_sg.security_group_id]
    kms_key_id                  = aws_kms_key.bi[0].arn
    min_capacity_units          = var.bi_dms_min_dcu
    max_capacity_units          = var.bi_dms_max_dcu
    multi_az                    = false
  }

  tags = local.tags

  lifecycle {
    # Same reasoning as the old task: DMS rewrites settings it did not get verbatim, which
    # would otherwise be a permanent diff.
    ignore_changes = [replication_settings]
  }
}

# We use 2 separate replica database modules here for backwards compatibility. Instead of morphing one resource as
# necessary for DMS or RDS Read Replica, which would make transitioning between the two settings on one stack impossible,
# we have two resources that can be created and deleted separately.

#tfsec:ignore:general-secrets-sensitive-in-variable
module "rds_replica_database" {
  source  = "terraform-aws-modules/rds/aws"
  version = "5.6.0"

  count = var.enable_bi && !local.dms_enabled && var.db_engine == "rds" ? 1 : 0

  identifier = "${local.identifier}-rds-replica"

  engine = "mysql"
  # var.rds_engine_family, i.e. the "8.0" PREFIX form, not an exact version. AWS resolves a
  # prefix to the latest matching minor, which is what lets auto_minor_version_upgrade move
  # these instances without Terraform fighting it. An exact pin here would be actively
  # dangerous: the default is 8.0.43 while live instances have drifted to 8.0.46, so it would
  # plan a DOWNGRADE that AWS rejects and every apply would fail.
  engine_version = var.rds_engine_family

  port                  = 3306
  instance_class        = data.aws_rds_orderable_db_instance.default.instance_class
  max_allocated_storage = var.rds_max_allocated_storage
  storage_type          = "gp3"
  replicate_source_db   = module.primary_database[0].db_instance_id
  storage_encrypted     = true
  # local.rds_kms_key_arn, not data.aws_kms_key.rds.arn: a same-region read replica inherits
  # the source's key regardless, so naming a different one was at best ignored. This tracks
  # the source once the primary is on a CMK.
  kms_key_id                  = local.rds_kms_key_arn
  apply_immediately           = local.db_apply_immediately
  publicly_accessible         = false
  allow_major_version_upgrade = true

  create_random_password = false

  // No need for multi-az for a read replica
  multi_az           = false
  ca_cert_identifier = local.ca_cert_identifier

  vpc_security_group_ids = [module.bi_database_sg.security_group_id]

  # Snapshot configuration
  deletion_protection = false
  skip_final_snapshot = true

  # DB parameter group
  create_db_parameter_group = false
  parameter_group_name      = aws_db_parameter_group.default[0].name

  create_db_option_group = false

  enabled_cloudwatch_logs_exports        = local.rds_instance_log_exports
  create_cloudwatch_log_group            = true
  cloudwatch_log_group_retention_in_days = 365

  tags = local.tags
}

# Aurora stacks have no instance-level parameter group to reuse (the cluster
# uses a cluster parameter group), so the DMS replica gets its own copy of
# aws_db_parameter_group.default. rds stacks keep sharing the primary's group -
# EXCEPT during an Aurora migration: the fence injects read_only=1 into the
# primary's group, and the DMS BI replica is a writable DMS target that must
# not be frozen with the source, so it moves to this group for the migration.
resource "aws_db_parameter_group" "bi_replica" {
  # Keep parameter parity with aws_db_parameter_group.default: a replica moved
  # here (aurora stacks always; rds stacks during a migration) must not silently
  # lose its slow/general log exports on the next reboot.
  count = local.dms_enabled && (var.db_engine == "aurora" || local.aurora_migration_active) ? 1 : 0

  name_prefix = "${local.identifier}-bi-"
  family      = "mysql${var.rds_engine_family}"

  parameter {
    name  = "slow_query_log"
    value = "1"
  }
  parameter {
    name  = "general_log"
    value = "1"
  }
  parameter {
    name  = "log_output"
    value = "FILE"
  }
  parameter {
    name  = "binlog_format"
    value = "ROW"
  }
  parameter {
    name  = "binlog_row_image"
    value = "Full"
  }
  parameter {
    name  = "binlog_checksum"
    value = "NONE"
  }
  parameter {
    name  = "group_concat_max_len"
    value = "33554432"
  }

  lifecycle {
    create_before_destroy = true
  }
}

# ---------------------------------------------------------------------------
# BI database, Aurora MySQL Serverless v2 (bi_db_engine = "aurora", set by _templates/env.hcl
# for new stacks; the code default stays "rds" so existing stacks are untouched)
#
# Replaces the provisioned MySQL 8.0 instance below. Two reasons: the old one was pinned to
# "8.0" with no input to change it, so every stack got an 8.0 BI database even when its primary
# was Aurora 8.4 - and RDS for MySQL 8.0 left standard support on 2026-07-31, so those bill RDS
# Extended Support per vCPU-hour from then on. Aurora MySQL 8.4 has standard support into 2029.
# And BI is bursty: Serverless v2 idles down to bi_aurora_min_acu (0.5 by default) between
# queries where a provisioned instance bills flat around the clock.
#
# This is a DMS TARGET, not a replica of anything - DMS full-loads into it - so it is
# rebuildable and safe to replace on a schedule. It is still an intentional replacement:
# db-replace-guard blocks the aws_db_instance destroy without the allow-db-replace label.
#
# Note what does NOT carry over: Aurora has no allocated_storage, so rds_allocated_storage /
# rds_max_allocated_storage / dms_allocated_storage do not apply here (storage grows on demand),
# and multi_az is expressed by adding instances rather than a flag. The DMS-vs-multi-AZ problem
# the legacy path worked around does not arise, because this cluster has a single writer.
resource "random_password" "bi_aurora" {
  count   = local.bi_uses_aurora ? 1 : 0
  length  = 40
  special = false
}

module "bi_aurora" {
  source  = "terraform-aws-modules/rds-aurora/aws"
  version = "10.2.0"

  count = local.bi_uses_aurora ? 1 : 0

  name           = "${local.identifier}-bi"
  engine         = "aurora-mysql"
  engine_mode    = "provisioned"
  engine_version = var.aurora_engine_version
  # Shares aurora_engine_version with the primary cluster, so it has to be able to follow the
  # same 8.0 -> 8.4 move. AWS rejects a cross-major ModifyDBCluster without this, which would
  # wedge every apply on a stack that bumps the version.
  allow_major_version_upgrade = true
  master_username             = local.db_username

  manage_master_user_password = false
  master_password_wo          = random_password.bi_aurora[0].result
  master_password_wo_version  = 1

  serverlessv2_scaling_configuration = {
    min_capacity = var.bi_aurora_min_acu
    max_capacity = var.bi_aurora_max_acu
  }

  # Single writer. publicly_accessible is an INSTANCE attribute on Aurora, not a cluster one,
  # so bi_public_access has to be set here rather than alongside the cluster settings. The
  # subnet group uses local.bi_subnet_ids, the same public-capable set the legacy instance used,
  # so a public BI endpoint keeps working.
  instances = {
    writer = {
      instance_class      = "db.serverless"
      publicly_accessible = var.bi_public_access
    }
  }

  # Reuse the BI security group (public CIDR allowlist + in-VPC access) rather than letting the
  # module invent one in the default VPC, which is how the primary aurora cluster fails.
  create_security_group  = false
  vpc_security_group_ids = [module.bi_database_sg.security_group_id]

  subnets                = local.bi_subnet_ids
  create_db_subnet_group = true

  storage_encrypted = true
  # Same key as the primary, for the same reason the legacy instance now follows it: this
  # database holds a full copy of production data and is the most exposed one in the stack when
  # bi_public_access is on. Aurora also re-encrypts on snapshot restore, so unlike the RDS path
  # this key is not a one-way door.
  kms_key_id = local.rds_kms_key_arn

  # Log exports match the primary aurora cluster, not the provisioned BI instances. Those two
  # only get local.rds_instance_log_exports because an instance cannot export the audit log
  # without the MARIADB_AUDIT_PLUGIN option group (rds.tf), and the comment there defers audit
  # coverage to "aurora covers audit" - a cluster here is exactly that case, so it takes the
  # full var.rds_log_exports. Without these three lines a stack switching BI to Aurora would
  # silently drop the query and audit trail its RDS BI database had, and land its logs in
  # auto-created never-expire groups.
  enabled_cloudwatch_logs_exports        = var.rds_log_exports
  create_cloudwatch_log_group            = true
  cloudwatch_log_group_retention_in_days = 365

  cluster_parameter_group = {
    family = local.aurora_param_family
    parameters = concat(
      [
        { name = "binlog_format", value = "ROW", apply_method = "pending-reboot" },
        # DMS full-loads MySQL-compatible targets with LOAD DATA LOCAL INFILE, which the server
        # refuses unless local_infile is on. Aurora MySQL already defaults it to 1, so this is a
        # pin rather than a fix - verified against aurora-mysql8.4: the parameter is settable at
        # cluster level, and writing 1 is dropped as a no-op precisely because 1 is the system
        # default (writing 0 does stick). Kept explicit because this cluster is a DMS target for
        # its whole life and a full load is how it gets rebuilt, so a group that someone had
        # turned off would break the rebuild silently and at the worst possible moment.
        { name = "local_infile", value = "1", apply_method = "immediate" },
      ],
      # Exports only ship what the engine writes, so the export list above is inert without
      # these. Same set and same conditions as the primary cluster in aurora.tf.
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

#tfsec:ignore:general-secrets-sensitive-in-variable
module "dms_replica_database" {
  source  = "terraform-aws-modules/rds/aws"
  version = "5.6.0"

  # Legacy provisioned path, and still what bi_db_engine defaults to, so every stack that has a
  # BI database today keeps it here until it deliberately moves to the Aurora cluster above.
  count = local.dms_enabled && !local.bi_uses_aurora ? 1 : 0

  identifier = "${local.identifier}-dms-replica"

  engine = "mysql"
  # Prefix form, same reasoning as the read replica above: exact-pinning this would plan a
  # downgrade on live instances that auto-minor-upgrade has already moved past the default.
  # This path is NOT where the 8.0 end-of-standard-support problem gets fixed - it is legacy by
  # definition now, and the fix is bi_db_engine = "aurora" (8.4), not a version bump here.
  engine_version = var.rds_engine_family

  port                  = 3306
  instance_class        = data.aws_rds_orderable_db_instance.default.instance_class
  allocated_storage     = var.rds_allocated_storage
  max_allocated_storage = var.rds_max_allocated_storage
  storage_type          = "gp3"
  storage_encrypted     = true
  # Follow the primary's key. This used to hardcode data.aws_kms_key.rds.arn, which meant the
  # BI database stayed on the AWS-managed key even on a stack whose primary correctly adopted a
  # CMK — and with bi_public_access this is the most exposed database in the stack. Changing it
  # forces a replacement whenever the primary moves to a CMK; the BI database is a rebuildable
  # DMS target, but the replace is still blocked by db-replace-guard, so do it deliberately
  # (out-of-band delete, then let Terraform create) alongside the primary's key swap.
  kms_key_id          = local.rds_kms_key_arn
  apply_immediately   = local.db_apply_immediately
  publicly_accessible = var.bi_public_access

  username               = "dozuki"
  random_password_length = 40
  create_random_password = true

  // Multi-az causes issues with DMS so we disable it.
  multi_az           = false
  ca_cert_identifier = local.ca_cert_identifier

  maintenance_window = "Sun:19:00-Sun:23:00"
  backup_window      = "17:00-19:00"

  vpc_security_group_ids = [module.bi_database_sg.security_group_id]

  # Snapshot configuration
  deletion_protection              = var.protect_resources
  skip_final_snapshot              = !var.protect_resources
  final_snapshot_identifier_prefix = "${local.identifier}-dms-replica" #Snapshot name upon DB deletion
  copy_tags_to_snapshot            = true

  # DB subnet group
  subnet_ids             = local.bi_subnet_ids
  create_db_subnet_group = true

  # DB parameter group
  create_db_parameter_group = false
  parameter_group_name      = var.db_engine == "rds" && !local.aurora_migration_active ? aws_db_parameter_group.default[0].name : aws_db_parameter_group.bi_replica[0].name

  create_db_option_group = false

  enabled_cloudwatch_logs_exports        = local.rds_instance_log_exports
  create_cloudwatch_log_group            = true
  cloudwatch_log_group_retention_in_days = 365

  tags = local.tags
}

resource "aws_secretsmanager_secret" "replica_database_credentials" {
  count = var.enable_bi ? 1 : 0

  name_prefix = "${local.identifier}-replica-database"

  recovery_window_in_days = var.protect_resources ? 7 : 0

  lifecycle {
    ignore_changes = [
      name,
      name_prefix
    ]
  }
}

resource "aws_secretsmanager_secret_version" "replica_database_credentials" {
  count = var.enable_bi ? 1 : 0

  secret_id = aws_secretsmanager_secret.replica_database_credentials[0].id
  secret_string = jsonencode({
    dbInstanceIdentifier = local.bi_db_identifier
    resourceId           = local.bi_db_resource_id
    host                 = local.bi_db_host
    port                 = local.bi_db_port
    engine               = "mysql"
    username             = local.bi_db_username
    password             = local.bi_db_password
  })
}