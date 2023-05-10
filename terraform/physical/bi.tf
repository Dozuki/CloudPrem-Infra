data "aws_iam_policy_document" "dms_assume_role" {
  count = local.dms_enabled ? 1 : 0

  statement {
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["dms.${data.aws_partition.current.dns_suffix}"]
    }

    actions = ["sts:AssumeRole"]
  }
}
resource "aws_iam_role" "dms-cloudwatch-logs-role" {
  count = local.dms_enabled ? 1 : 0

  assume_role_policy = data.aws_iam_policy_document.dms_assume_role[0].json
  name               = "${local.identifier}-${data.aws_region.current.name}-dms-cloudwatch-logs-role"
}

resource "aws_iam_role_policy_attachment" "dms-cloudwatch-logs-role-AmazonDMSCloudWatchLogsRole" {
  count = local.dms_enabled ? 1 : 0

  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonDMSCloudWatchLogsRole"
  role       = aws_iam_role.dms-cloudwatch-logs-role[0].name
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
  replication_instance_class = "dms.r5.large"
  allocated_storage          = var.rds_allocated_storage
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
  extra_connection_attributes = "afterConnectScript=call mysql.rds_set_configuration('binlog retention hours', 24);"
  port                        = 3306
  kms_key_arn                 = aws_kms_key.bi[0].arn

  username    = module.replica_database[0].db_instance_username
  password    = module.replica_database[0].db_instance_password
  server_name = module.replica_database[0].db_instance_address

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