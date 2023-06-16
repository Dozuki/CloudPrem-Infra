moved {
  from = module.database_sg
  to   = module.primary_database_sg
}
moved {
  from = aws_secretsmanager_secret.replica_database[0]
  to   = aws_secretsmanager_secret.replica_database_credentials[0]
}
moved {
  from = aws_secretsmanager_secret_version.replica_database[0]
  to   = aws_secretsmanager_secret_version.replica_database_credentials[0]
}
moved {
  from = random_password.replica_database[0]
  to   = module.replica_database[0].random_password.master_password[0]
}
moved {
  from = random_password.primary_database
  to   = module.primary_database.random_password.master_password[0]
}

data "aws_kms_key" "rds" {
  key_id = var.rds_kms_key_id
}
module "primary_database_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "4.17.1"

  name            = "${local.identifier}-database"
  use_name_prefix = false
  # Do not modify the description. Doing so triggers a full recreate (which fails) due to an AWS bug.
  description = "Security group for ${local.identifier}. Allows access from within the VPC on port 3306"
  vpc_id      = local.vpc_id

  ingress_with_source_security_group_id = [
    {
      rule                     = "mysql-tcp"
      source_security_group_id = module.eks_cluster.worker_security_group_id
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
  version = "4.17.1"

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

resource "aws_db_parameter_group" "default" {

  name_prefix = local.identifier
  family      = "mysql8.0"

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
}

#tfsec:ignore:general-secrets-sensitive-in-variable
module "primary_database" {
  source  = "terraform-aws-modules/rds/aws"
  version = "5.6.0"

  identifier = local.identifier

  engine         = "mysql"
  engine_version = "8.0"

  port                  = 3306
  instance_class        = var.rds_instance_type
  allocated_storage     = var.rds_allocated_storage
  max_allocated_storage = var.rds_max_allocated_storage
  storage_encrypted     = true
  kms_key_id            = data.aws_kms_key.rds.arn
  apply_immediately     = !var.protect_resources

  username               = "dozuki"
  random_password_length = 40

  multi_az           = var.rds_multi_az
  ca_cert_identifier = local.ca_cert_identifier

  maintenance_window      = "Sun:19:00-Sun:23:00"
  backup_window           = "17:00-19:00"
  backup_retention_period = var.rds_backup_retention_period

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
  parameter_group_name      = aws_db_parameter_group.default.name

  create_db_option_group = false

  tags = local.tags
}

#tfsec:ignore:aws-ssm-secret-use-customer-key
resource "aws_secretsmanager_secret" "primary_database_credentials" {
  name_prefix = "${local.identifier}-database"

  recovery_window_in_days = 0
  //  kms_key_id              = data.aws_kms_key.rds.arn
}

resource "aws_secretsmanager_secret_version" "primary_database_credentials" {
  secret_id = aws_secretsmanager_secret.primary_database_credentials.id
  secret_string = jsonencode({
    dbInstanceIdentifier = module.primary_database.db_instance_id
    resourceId           = module.primary_database.db_instance_resource_id
    host                 = module.primary_database.db_instance_address
    port                 = module.primary_database.db_instance_port
    engine               = "mysql"
    username             = module.primary_database.db_instance_username
    password             = module.primary_database.db_instance_password
  })
}