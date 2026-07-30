
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
}

resource "aws_dms_replication_instance" "this" {
  count = local.dms_enabled ? 1 : 0

  replication_instance_id    = local.identifier
  replication_instance_class = var.dms_instance_type
  allocated_storage          = var.dms_allocated_storage
  kms_key_arn                = aws_kms_key.bi[0].arn
  auto_minor_version_upgrade = true

  publicly_accessible         = false
  replication_subnet_group_id = aws_dms_replication_subnet_group.this[0].id

  vpc_security_group_ids = [module.bi_database_sg.security_group_id]

  tags = local.tags

}

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

  # Engine-agnostic (main.tf): the BI target is an Aurora Serverless cluster by default and a
  # provisioned instance on the legacy path. engine_name stays "mysql" - DMS treats Aurora MySQL
  # as a wire-compatible MySQL target.
  username    = local.bi_db_username
  password    = local.bi_db_password
  server_name = local.bi_db_host

  tags = local.tags
}

resource "aws_dms_replication_task" "this" {
  count = local.dms_enabled ? 1 : 0

  replication_task_id       = local.identifier
  migration_type            = "full-load-and-cdc"
  replication_instance_arn  = aws_dms_replication_instance.this[0].replication_instance_arn
  table_mappings            = file("static/dms_mapping.json")
  replication_task_settings = file("static/dms_config.json")

  source_endpoint_arn = aws_dms_endpoint.source[0].endpoint_arn
  target_endpoint_arn = aws_dms_endpoint.target[0].endpoint_arn

  tags = local.tags

  lifecycle {
    ignore_changes = [replication_task_settings]
  }
}

# AWS provider issue to replace this https://github.com/hashicorp/terraform-provider-aws/issues/2083
resource "null_resource" "replication_control" {
  count = local.dms_enabled ? 1 : 0

  triggers = {
    dms_task_arn        = aws_dms_replication_task.this[0].replication_task_arn,
    source_endpoint_arn = aws_dms_endpoint.source[0].endpoint_arn,
    target_endpoint_arn = aws_dms_endpoint.target[0].endpoint_arn,
    aws_region          = data.aws_region.current.region,
    aws_profile         = var.aws_profile
  }

  provisioner "local-exec" {
    when    = destroy
    command = "/usr/bin/env bash ./util/dms-stop.sh ${self.triggers["dms_task_arn"]} ${self.triggers["aws_region"]} ${self.triggers["aws_profile"]}"
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
# BI database, Aurora MySQL Serverless v2 (bi_db_engine = "aurora", the default)
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

  name            = "${local.identifier}-bi"
  engine          = "aurora-mysql"
  engine_mode     = "provisioned"
  engine_version  = var.aurora_engine_version
  master_username = local.db_username

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

  cluster_parameter_group = {
    family = local.aurora_param_family
    parameters = [
      { name = "binlog_format", value = "ROW", apply_method = "pending-reboot" },
    ]
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

  # Legacy provisioned path. Only when the stack pins bi_db_engine = "rds"; new stacks get the
  # Aurora Serverless cluster above.
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