
data "aws_iam_role" "dms-vpc-role" {
  count = local.dms_enabled ? try(length(data.aws_iam_roles.dms-vpc-roles.arns), 0) > 0 ? 1 : 0 : 0

  name = "dms-vpc-role"
}
data "aws_iam_roles" "dms-vpc-roles" {
  name_regex = "dms-vpc-role"
}
data "aws_iam_role" "dms-cloudwatch-role" {
  count = local.dms_enabled ? try(length(data.aws_iam_roles.dms-cloudwatch-roles.arns), 0) > 0 ? 1 : 0 : 0

  name = "dms-cloudwatch-logs-role"
}
data "aws_iam_roles" "dms-cloudwatch-roles" {
  name_regex = "dms-cloudwatch-logs-role"
}
# We create the dms-vpc-role and dms-cloudwatch-logs-role using a null_resource to prevent the removal of the
# account-wide role should this stack be deleted. In other words, to keep the role out of the state.
resource "null_resource" "create_dms_vpc_role" {
  count = local.dms_enabled ? length(data.aws_iam_role.dms-vpc-role) > 0 ? 0 : 1 : 0

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
  count = local.dms_enabled ? length(data.aws_iam_role.dms-cloudwatch-role) > 0 ? 0 : 1 : 0

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

  description         = "BI KMS key for replication credentials"
  enable_key_rotation = true
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

  username    = module.primary_database.db_instance_username
  password    = module.primary_database.db_instance_password
  server_name = module.primary_database.db_instance_address

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

  username    = module.dms_replica_database[0].db_instance_username
  password    = module.dms_replica_database[0].db_instance_password
  server_name = module.dms_replica_database[0].db_instance_address

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
    aws_region          = data.aws_region.current.name,
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

  count = var.enable_bi ? local.dms_enabled ? 0 : 1 : 0

  identifier = "${local.identifier}-rds-replica"

  engine         = "mysql"
  engine_version = "8.0"

  port                        = 3306
  instance_class              = data.aws_rds_orderable_db_instance.default.instance_class
  max_allocated_storage       = var.rds_max_allocated_storage
  replicate_source_db         = module.primary_database.db_instance_id
  storage_encrypted           = true
  kms_key_id                  = data.aws_kms_key.rds.arn
  apply_immediately           = !var.protect_resources
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
  parameter_group_name      = aws_db_parameter_group.default.name

  create_db_option_group = false

  tags = local.tags
}

#tfsec:ignore:general-secrets-sensitive-in-variable
module "dms_replica_database" {
  source  = "terraform-aws-modules/rds/aws"
  version = "5.6.0"

  count = local.dms_enabled ? 1 : 0

  identifier = "${local.identifier}-dms-replica"

  engine         = "mysql"
  engine_version = "8.0"

  port                  = 3306
  instance_class        = data.aws_rds_orderable_db_instance.default.instance_class
  allocated_storage     = var.rds_allocated_storage
  max_allocated_storage = var.rds_max_allocated_storage
  storage_encrypted     = true
  kms_key_id            = data.aws_kms_key.rds.arn
  apply_immediately     = !var.protect_resources
  publicly_accessible   = var.bi_public_access

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
  parameter_group_name      = aws_db_parameter_group.default.name

  create_db_option_group = false

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
    dbInstanceIdentifier = local.bi_db.db_instance_id
    resourceId           = local.bi_db.db_instance_resource_id
    host                 = local.bi_db.db_instance_address
    port                 = local.bi_db.db_instance_port
    engine               = "mysql"
    username             = local.dms_enabled ? module.dms_replica_database[0].db_instance_username : module.primary_database.db_instance_username
    password             = local.dms_enabled ? module.dms_replica_database[0].db_instance_password : module.primary_database.db_instance_password
  })
}