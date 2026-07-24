data "aws_rds_orderable_db_instance" "default" {
  engine         = "mysql"
  engine_version = var.rds_engine_version

  supports_enhanced_monitoring = true
  supports_storage_autoscaling = true
  supports_storage_encryption  = true
  vpc                          = true

  preferred_instance_classes = var.rds_preferred_instance_classes

  lifecycle {
    postcondition {
      condition     = contains(keys(local.rds_instance_memory), self.instance_class)
      error_message = "RDS instance type not supported. Please adjust the var.rds_preferred_instance_classes. All values must exist in local.rds_instance_memory."
    }
  }
}

data "aws_kms_key" "rds" {
  key_id = var.rds_kms_key_id
}
module "primary_database_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "~> 5.0"

  name            = "${local.identifier}-database"
  use_name_prefix = false
  # Do not modify the description. Doing so triggers a full recreate (which fails) due to an AWS bug.
  description = "Security group for ${local.identifier}. Allows access from within the VPC on port 3306"
  vpc_id      = local.vpc_id

  ingress_with_source_security_group_id = [
    {
      rule                     = "mysql-tcp"
      source_security_group_id = module.eks_cluster.cluster_primary_security_group_id
    },
    {
      rule                     = "mysql-tcp"
      source_security_group_id = module.bastion_sg.security_group_id
    },
    {
      rule                     = "mysql-tcp"
      source_security_group_id = module.bi_database_sg.security_group_id
    },
  ]

  tags = local.tags
}
# To make the terraform a bit easier we will always create this security group even if BI is disabled.
module "bi_database_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "~> 5.0"

  name            = "${local.identifier}-bi-database"
  use_name_prefix = false
  description     = "Security group for bi access on ${local.identifier}. Allows access from specified CIDRs on port 3306"
  vpc_id          = local.vpc_id

  ingress_rules       = ["mysql-tcp"]
  ingress_cidr_blocks = local.bi_access_cidrs

  ingress_with_source_security_group_id = [
    {
      rule                     = "mysql-tcp"
      source_security_group_id = module.bastion_sg.security_group_id
    }
  ]

  ingress_with_self = [
    {
      rule = "all-all"
    }
  ]

  egress_with_source_security_group_id = [
    {
      rule                     = "mysql-tcp"
      source_security_group_id = module.primary_database_sg.security_group_id
    }
  ]

  egress_with_self = [
    {
      rule = "all-all"
    }
  ]

  tags = local.tags
}

locals {
  # Provisioned instances can export error/general/slowquery without extra
  # setup; audit needs the MARIADB_AUDIT_PLUGIN option group, which nothing
  # here manages (create_db_option_group = false) - known gap, aurora covers
  # audit. Shared by the primary and the two BI replicas.
  rds_instance_log_exports = sort(setintersection(var.rds_log_exports, ["error", "general", "slowquery"]))
}

resource "aws_db_parameter_group" "default" {
  count = var.db_engine == "rds" ? 1 : 0

  name_prefix = local.identifier
  family      = "mysql${var.rds_engine_family}"

  # Make the exported logs exist (exports only ship what the engine writes).
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

#tfsec:ignore:general-secrets-sensitive-in-variable
module "primary_database" {
  source  = "terraform-aws-modules/rds/aws"
  version = "5.6.0"

  count = var.db_engine == "rds" ? 1 : 0

  identifier = local.identifier

  engine         = "mysql"
  engine_version = var.rds_engine_family

  port                         = 3306
  instance_class               = data.aws_rds_orderable_db_instance.default.instance_class
  allocated_storage            = var.rds_allocated_storage
  max_allocated_storage        = var.rds_max_allocated_storage
  storage_type                 = "gp3"
  storage_encrypted            = true
  kms_key_id                   = local.rds_kms_key_arn
  apply_immediately            = !var.protect_resources
  allow_major_version_upgrade  = true
  performance_insights_enabled = true
  # iam_database_authentication_enabled = true

  username               = "dozuki"
  random_password_length = 40

  multi_az           = var.rds_multi_az
  ca_cert_identifier = local.ca_cert_identifier

  maintenance_window      = "Sun:19:00-Sun:23:00"
  backup_window           = "17:00-19:00"
  backup_retention_period = var.rds_backup_retention_period

  monitoring_interval             = 30
  create_monitoring_role          = true
  monitoring_role_use_name_prefix = true

  vpc_security_group_ids = [module.primary_database_sg.security_group_id]

  # Snapshot configuration
  deletion_protection              = var.protect_resources
  snapshot_identifier              = var.rds_snapshot_identifier # Restore from snapshot
  skip_final_snapshot              = !var.protect_resources
  final_snapshot_identifier_prefix = local.identifier # Snapshot name upon DB deletion
  copy_tags_to_snapshot            = true

  # DB subnet group
  subnet_ids             = local.private_subnet_ids
  create_db_subnet_group = true


  # DB parameter group
  create_db_parameter_group = false
  parameter_group_name      = aws_db_parameter_group.default[0].name

  create_db_option_group = false

  # Default-on log exports with retained log groups (never-expire otherwise).
  enabled_cloudwatch_logs_exports        = local.rds_instance_log_exports
  create_cloudwatch_log_group            = true
  cloudwatch_log_group_retention_in_days = 365

  tags = local.tags
}

#tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_secretsmanager_secret" "primary_database_credentials" {
  name_prefix = "${local.identifier}-database"

  recovery_window_in_days = var.protect_resources ? 7 : 0

  lifecycle {
    ignore_changes = [
      name,
      name_prefix
    ]
  }
}

resource "aws_secretsmanager_secret_version" "primary_database_credentials" {
  secret_id = aws_secretsmanager_secret.primary_database_credentials.id
  secret_string = jsonencode({
    dbInstanceIdentifier = local.db_identifier
    resourceId           = local.db_resource_id
    host                 = local.db_host
    port                 = local.db_port
    engine               = "mysql"
    username             = local.db_username
    password             = local.db_password
  })
}