terraform {
  required_version = ">= 1.2.0"

  required_providers {
    aws = "4.57.0"
  }
}

locals {
  # Tags for all resources. If you add a tag, it must never be blank.
  tags = {
    Terraform   = "nodelete"
    Project     = "Dozuki"
    Environment = "bootstrap"
  }
}

resource "aws_ssm_parameter" "customer_ids" {
  for_each = var.customer_id_parameters

  name        = "/dozuki/workstation/kots/${each.key}/customer_id"
  description = "Customer ID for ${each.key} deployments"
  type        = "SecureString"
  value       = each.value

  tags = local.tags
}

data "aws_iam_policy_document" "dms_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      identifiers = ["dms.amazonaws.com"]
      type        = "Service"
    }
  }
}

resource "aws_iam_role" "dms-access-for-endpoint" {
  count              = var.dms_setup ? 1 : 0
  assume_role_policy = data.aws_iam_policy_document.dms_assume_role.json
  name               = "dms-access-for-endpoint"
}

resource "aws_iam_role_policy_attachment" "dms-access-for-endpoint-AmazonDMSRedshiftS3Role" {
  count = var.dms_setup ? 1 : 0

  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonDMSRedshiftS3Role"
  role       = aws_iam_role.dms-access-for-endpoint[0].name
}

resource "aws_iam_role" "dms-cloudwatch-logs-role" {
  count = var.dms_setup ? 1 : 0

  assume_role_policy = data.aws_iam_policy_document.dms_assume_role.json
  name               = "dms-cloudwatch-logs-role"
}

resource "aws_iam_role_policy_attachment" "dms-cloudwatch-logs-role-AmazonDMSCloudWatchLogsRole" {
  count = var.dms_setup ? 1 : 0

  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonDMSCloudWatchLogsRole"
  role       = aws_iam_role.dms-cloudwatch-logs-role[0].name
}

resource "aws_iam_role" "dms-vpc-role" {
  count = var.dms_setup ? 1 : 0

  assume_role_policy = data.aws_iam_policy_document.dms_assume_role.json
  name               = "dms-vpc-role"
}

resource "aws_iam_role_policy_attachment" "dms-vpc-role-AmazonDMSVPCManagementRole" {
  count = var.dms_setup ? 1 : 0

  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonDMSVPCManagementRole"
  role       = aws_iam_role.dms-vpc-role[0].name
}


