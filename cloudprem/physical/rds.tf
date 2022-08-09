module "primary_database_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "4.9.0"

  name            = "${local.identifier}-database"
  use_name_prefix = false
  description     = "Security group for ${local.identifier}s primary database. Allows access from worker nodes, bastion, and bi database on port 3306"
  vpc_id          = local.vpc_id

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
  version = "4.9.0"

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

resource "random_password" "primary_database" {
  length  = 40
  special = false
}

resource "aws_db_parameter_group" "bi" {
  count = var.enable_bi ? 1 : 0

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

resource "aws_db_parameter_group" "default" {

  name_prefix = local.identifier
  family      = "mysql8.0"

  parameter {
    name  = "group_concat_max_len"
    value = "33554432"
  }
}

#tfsec:ignore:general-secrets-sensitive-in-variable
module "primary_database" {
  source  = "terraform-aws-modules/rds/aws"
  version = "5.0.1"

  identifier = local.identifier

  engine                = "mysql"
  engine_version        = "8.0"
  port                  = 3306
  instance_class        = var.rds_instance_type
  allocated_storage     = var.rds_allocated_storage
  max_allocated_storage = var.rds_max_allocated_storage
  storage_encrypted     = true
  kms_key_id            = data.aws_kms_key.rds.arn
  apply_immediately     = !var.protect_resources

  username = "dozuki"
  password = random_password.primary_database.result

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
  create_random_password           = false

  # DB subnet group
  # db_subnet_group_name = local.identifier # https://github.com/terraform-aws-modules/terraform-aws-rds/issues/42
  subnet_ids             = local.private_subnet_ids
  create_db_subnet_group = true

  # DB parameter group
  create_db_parameter_group = false
  parameter_group_name      = local.rds_parameter_group_name

  # DB option group
  option_group_name      = null
  create_db_option_group = false # https://github.com/terraform-aws-modules/terraform-aws-rds/issues/188

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
    password             = random_password.primary_database.result
  })
}

#  ############### BI ##############

resource "random_password" "replica_database" {
  count = var.enable_bi ? 1 : 0

  length  = 40
  special = false
}

#tfsec:ignore:general-secrets-sensitive-in-variable
module "replica_database" {
  source  = "terraform-aws-modules/rds/aws"
  version = "5.0.1"

  count = var.enable_bi ? 1 : 0

  identifier = "${local.identifier}-replica"

  engine                = "mysql"
  engine_version        = "8.0"
  port                  = 3306
  instance_class        = var.rds_instance_type
  allocated_storage     = var.rds_allocated_storage
  max_allocated_storage = var.rds_max_allocated_storage
  storage_encrypted     = true
  kms_key_id            = data.aws_kms_key.rds.arn
  apply_immediately     = !var.protect_resources
  publicly_accessible   = var.bi_public_access

  username = "dozuki"
  password = random_password.replica_database[0].result

  multi_az           = var.rds_multi_az
  ca_cert_identifier = local.ca_cert_identifier

  maintenance_window = "Sun:19:00-Sun:23:00"
  backup_window      = "17:00-19:00"
  # backup_retention_period = var.rds_backup_retention_period

  vpc_security_group_ids = [module.bi_database_sg.security_group_id]

  # Snapshot configuration
  deletion_protection              = var.protect_resources
  skip_final_snapshot              = !var.protect_resources
  final_snapshot_identifier_prefix = "${local.identifier}-replica" # Snapshot name upon DB deletion
  copy_tags_to_snapshot            = true
  create_random_password           = false
  # DB subnet group
  # db_subnet_group_name = local.identifier # https://github.com/terraform-aws-modules/terraform-aws-rds/issues/42
  subnet_ids             = local.bi_subnet_ids
  create_db_subnet_group = true

  # DB parameter group
  create_db_parameter_group = false
  parameter_group_name      = local.rds_parameter_group_name

  # DB option group
  option_group_name      = null
  create_db_option_group = false # https://github.com/terraform-aws-modules/terraform-aws-rds/issues/188

  tags = local.tags
}

resource "aws_secretsmanager_secret" "replica_database_credentials" {
  count = var.enable_bi ? 1 : 0

  name_prefix = "${local.identifier}-replica-database"

  recovery_window_in_days = var.protect_resources ? 7 : 0
}

resource "aws_secretsmanager_secret_version" "replica_database_credentials" {
  count = var.enable_bi ? 1 : 0

  secret_id = aws_secretsmanager_secret.replica_database_credentials[0].id
  secret_string = jsonencode({
    dbInstanceIdentifier = module.replica_database[0].db_instance_id
    resourceId           = module.replica_database[0].db_instance_resource_id
    host                 = module.replica_database[0].db_instance_address
    port                 = module.replica_database[0].db_instance_port
    engine               = "mysql"
    username             = module.replica_database[0].db_instance_username
    password             = random_password.replica_database[0].result
  })
}

resource "aws_dms_replication_subnet_group" "this" {
  count = var.enable_bi ? 1 : 0

  replication_subnet_group_id          = "${local.identifier}-replication"
  replication_subnet_group_description = "${local.identifier} replication subnet group"

  subnet_ids = local.private_subnet_ids

  tags = local.tags
}

resource "aws_kms_key" "bi" {
  count = var.enable_bi ? 1 : 0

  description         = "BI KMS key for replication credentials"
  enable_key_rotation = true
}

resource "aws_dms_replication_instance" "this" {
  count = var.enable_bi ? 1 : 0

  replication_instance_id    = local.identifier
  replication_instance_class = "dms.r5.large"
  engine_version             = "3.4.6"
  allocated_storage          = var.rds_allocated_storage
  kms_key_arn                = aws_kms_key.bi[0].arn
  auto_minor_version_upgrade = true

  publicly_accessible         = false
  replication_subnet_group_id = aws_dms_replication_subnet_group.this[0].id

  vpc_security_group_ids = [module.bi_database_sg.security_group_id]

  tags = local.tags

}

resource "aws_dms_certificate" "this" {
  count = var.enable_bi ? 1 : 0

  certificate_id  = "${local.identifier}-dms-certificate"
  certificate_pem = file(local.ca_cert_pem_file)

  tags = local.tags

}

resource "aws_dms_endpoint" "source" {
  count = var.enable_bi ? 1 : 0

  endpoint_id                 = "${local.identifier}-source"
  certificate_arn             = aws_dms_certificate.this[0].certificate_arn
  ssl_mode                    = "verify-full"
  endpoint_type               = "source"
  engine_name                 = "mysql"
  extra_connection_attributes = "afterConnectScript=call mysql.rds_set_configuration('binlog retention hours', 24);"
  port                        = 3306
  kms_key_arn                 = aws_kms_key.bi[0].arn

  username    = module.primary_database.db_instance_username
  password    = random_password.primary_database.result
  server_name = module.primary_database.db_instance_address

  tags = local.tags
}

resource "aws_dms_endpoint" "target" {
  count = var.enable_bi ? 1 : 0

  endpoint_id                 = "${local.identifier}-target"
  certificate_arn             = aws_dms_certificate.this[0].certificate_arn
  ssl_mode                    = "verify-full"
  endpoint_type               = "target"
  engine_name                 = "mysql"
  extra_connection_attributes = "afterConnectScript=call mysql.rds_set_configuration('binlog retention hours', 24);"
  port                        = 3306
  kms_key_arn                 = aws_kms_key.bi[0].arn

  username    = module.replica_database[0].db_instance_username
  password    = random_password.replica_database[0].result
  server_name = module.replica_database[0].db_instance_address

  tags = local.tags
}

resource "aws_dms_replication_task" "this" {
  count = var.enable_bi ? 1 : 0

  replication_task_id      = local.identifier
  migration_type           = "full-load-and-cdc"
  replication_instance_arn = aws_dms_replication_instance.this[0].replication_instance_arn
  table_mappings           = file("static/dms_mapping.json")
  start_replication_task   = false

  replication_task_settings = file("static/dms_config.json")

  source_endpoint_arn = aws_dms_endpoint.source[0].endpoint_arn
  target_endpoint_arn = aws_dms_endpoint.target[0].endpoint_arn

  tags = local.tags

  lifecycle {
    ignore_changes = [replication_task_settings]
  }
}

# The DMS task block above is now capable of starting and stopping itself but due to the async nature of our app
# deployment we need to tightly control when it starts so we can't use it; we have to control start and stop manually.
resource "null_resource" "replication_control" {
  count = var.enable_bi ? 1 : 0

  triggers = {
    dms_task_arn        = aws_dms_replication_task.this[0].replication_task_arn,
    source_endpoint_arn = aws_dms_endpoint.source[0].endpoint_arn,
    target_endpoint_arn = aws_dms_endpoint.target[0].endpoint_arn,
    aws_region          = data.aws_region.current.name,
    aws_profile         = var.aws_profile
  }

  provisioner "local-exec" {
    when    = destroy
    command = "/usr/bin/env bash ./util/dms-stop.sh ${self.triggers["dms_task_arn"]} ${self.triggers["aws_region"]} ${self.triggers["aws_profile"]}"
  }
}