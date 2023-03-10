data "aws_iam_policy_document" "vpn-logs-kms" {
  statement {
    sid    = "Enable IAM User Permissions"
    effect = "Allow"
    principals {
      identifiers = ["arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:root"]
      type        = "AWS"
    }
    actions   = ["kms:*"]
    resources = ["*"]
  }
  statement {
    effect = "Allow"
    principals {
      identifiers = ["logs.${data.aws_region.current.name}.amazonaws.com"]
      type        = "Service"
    }
    actions   = ["kms:*"]
    resources = ["*"]
  }
}
resource "aws_kms_key" "vpn-logs" {
  description         = "VPN Log Secret Encryption Key"
  enable_key_rotation = true

  policy = data.aws_iam_policy_document.vpn-logs-kms.json

  tags = local.tags
}
resource "aws_cloudwatch_log_group" "vpn-logs" {
  name_prefix       = "${local.identifier}-vpn"
  retention_in_days = 30
  kms_key_id        = aws_kms_key.vpn-logs.arn

  tags = local.tags
}
resource "aws_cloudwatch_log_stream" "vpn-logs-stream" {
  name           = "connection_logs"
  log_group_name = aws_cloudwatch_log_group.vpn-logs.name
}