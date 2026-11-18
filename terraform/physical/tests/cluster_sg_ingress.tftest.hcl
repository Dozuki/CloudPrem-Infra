mock_provider "aws" {
  mock_resource "aws_iam_policy" { defaults = { arn = "arn:aws:iam::123456789012:policy/stub" } }
  mock_resource "aws_iam_role" { defaults = { arn = "arn:aws:iam::123456789012:role/stub" } }
  mock_resource "aws_iam_instance_profile" { defaults = { arn = "arn:aws:iam::123456789012:instance-profile/stub" } }
  mock_resource "aws_sns_topic" { defaults = { arn = "arn:aws:sns:us-east-1:123456789012:stub" } }
  mock_resource "aws_cloudwatch_log_group" { defaults = { arn = "arn:aws:logs:us-east-1:123456789012:log-group:stub" } }
  mock_resource "aws_kms_key" { defaults = { arn = "arn:aws:kms:us-east-1:123456789012:key/stub" } }
  mock_resource "aws_vpc_endpoint" { defaults = { dns_entry = [{ dns_name = "x.example.com", hosted_zone_id = "Z1" }] } }
  mock_resource "aws_opensearch_domain" { defaults = { arn = "arn:aws:es:us-east-1:123456789012:domain/stub" } }
  mock_resource "aws_secretsmanager_secret" { defaults = { arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:stub" } }
  mock_data "aws_kms_key" { defaults = { arn = "arn:aws:kms:us-east-1:123456789012:key/stub" } }
  mock_data "aws_iam_session_context" { defaults = { issuer_arn = "arn:aws:iam::123456789012:role/stub" } }
  mock_resource "aws_ssm_document" { defaults = { latest_version = "1" } }
  mock_resource "aws_launch_template" { defaults = { id = "lt-0123456789abcdef0", arn = "arn:aws:ec2:us-east-1:123456789012:launch-template/lt-0123456789abcdef0" } }
  mock_resource "aws_eks_cluster" { defaults = { arn = "arn:aws:eks:us-east-1:123456789012:cluster/stub", identity = [{ oidc = [{ issuer = "https://oidc.eks.us-east-1.amazonaws.com/id/STUB" }] }], certificate_authority = [{ data = "c3R1Yg==" }] } }
  mock_resource "aws_lb" { defaults = { arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/net/stub/0123456789abcdef" } }
  mock_resource "aws_lb_target_group" { defaults = { arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/stub/0123456789abcdef" } }
  mock_data "aws_availability_zones" { defaults = { names = ["us-east-1a", "us-east-1b", "us-east-1c"] } }
  mock_data "aws_iam_policy_document" { defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" } }
  mock_data "aws_partition" { defaults = { partition = "aws", dns_suffix = "amazonaws.com" } }
  mock_data "aws_region" { defaults = { region = "us-east-1", name = "us-east-1" } }
  mock_data "aws_caller_identity" { defaults = { account_id = "123456789012", arn = "arn:aws:iam::123456789012:role/stub" } }
}
mock_provider "aws" {
  alias = "dns"
}
mock_provider "aws" {
  alias = "dr"
  mock_data "aws_region" { defaults = { region = "us-west-2", name = "us-west-2" } }
}
mock_provider "random" {}
mock_provider "null" {}
mock_provider "archive" {}
mock_provider "tls" {
  mock_data "tls_certificate" { defaults = { certificates = [{ sha1_fingerprint = "1234567890121234567890121234567890120000" }] } }
}
mock_provider "time" {}
mock_provider "kubernetes" {}
mock_provider "cloudinit" {}

variables {
  vault_endpoint_service_name = "com.amazonaws.vpce.us-east-1.vpce-svc-stub"
  alarm_email                 = "a@example.com"
  environment                 = "test"
  enable_dr                   = false
}

run "created_vpc" {
  command = plan
  variables { vpc_cidr = "10.20.0.0/16" }
  assert {
    condition     = aws_security_group_rule.nlb_to_envoy_https_shifted.cidr_blocks == tolist(["10.20.0.0/20", "10.20.16.0/20", "10.20.32.0/20"]) && aws_security_group_rule.nlb_to_envoy_http_shifted.cidr_blocks == tolist(["10.20.0.0/20", "10.20.16.0/20", "10.20.32.0/20"])
    error_message = "rules should allow exactly the public subnet CIDRs"
  }
  assert {
    condition     = !contains(aws_security_group_rule.nlb_to_envoy_https_shifted.cidr_blocks, "0.0.0.0/0") && !contains(aws_security_group_rule.nlb_to_envoy_http_shifted.cidr_blocks, "0.0.0.0/0")
    error_message = "rules must not allow 0.0.0.0/0"
  }
  assert {
    condition     = aws_security_group_rule.nlb_to_envoy_https_shifted.from_port == 10443 && aws_security_group_rule.nlb_to_envoy_https_shifted.to_port == 10443 && aws_security_group_rule.nlb_to_envoy_http_shifted.from_port == 10080 && aws_security_group_rule.nlb_to_envoy_http_shifted.to_port == 10080
    error_message = "rules should use the shifted ports"
  }
}

run "byo_vpc" {
  command = plan
  variables { vpc_id = "vpc-0123456789abcdef0" }
  override_data {
    target = data.aws_vpc.this
    values = { cidr_block = "10.0.0.0/16" }
  }
  override_data {
    target = data.aws_subnets.private
    values = { ids = ["subnet-0123456789abcdef8", "subnet-0123456789abcdef9"] }
  }
  override_data {
    target = data.aws_subnets.public
    values = { ids = ["subnet-0123456789abcdef0", "subnet-0123456789abcdef1", "subnet-0123456789abcdef2"] }
  }
  # Per-instance override addresses are rejected, so every instance gets the same CIDR.
  override_data {
    target = data.aws_subnet.public
    values = { cidr_block = "10.0.0.0/24" }
  }
  assert {
    condition     = aws_security_group_rule.nlb_to_envoy_https_shifted.cidr_blocks == tolist(["10.0.0.0/24", "10.0.0.0/24", "10.0.0.0/24"]) && aws_security_group_rule.nlb_to_envoy_http_shifted.cidr_blocks == tolist(["10.0.0.0/24", "10.0.0.0/24", "10.0.0.0/24"])
    error_message = "rules should allow exactly the public subnet CIDRs"
  }
  assert {
    condition     = !contains(aws_security_group_rule.nlb_to_envoy_https_shifted.cidr_blocks, "0.0.0.0/0") && !contains(aws_security_group_rule.nlb_to_envoy_http_shifted.cidr_blocks, "0.0.0.0/0")
    error_message = "rules must not allow 0.0.0.0/0"
  }
  assert {
    condition     = aws_security_group_rule.nlb_to_envoy_https_shifted.from_port == 10443 && aws_security_group_rule.nlb_to_envoy_https_shifted.to_port == 10443 && aws_security_group_rule.nlb_to_envoy_http_shifted.from_port == 10080 && aws_security_group_rule.nlb_to_envoy_http_shifted.to_port == 10080
    error_message = "rules should use the shifted ports"
  }
}

run "byo_vpc_no_public_subnets" {
  command = plan
  variables { vpc_id = "vpc-0123456789abcdef0" }
  override_data {
    target = data.aws_vpc.this
    values = { cidr_block = "10.0.0.0/16" }
  }
  override_data {
    target = data.aws_subnets.private
    values = { ids = ["subnet-0123456789abcdef8", "subnet-0123456789abcdef9"] }
  }
  override_data {
    target = data.aws_subnets.public
    values = { ids = [] }
  }
  expect_failures = [
    aws_security_group_rule.nlb_to_envoy_https_shifted,
    aws_security_group_rule.nlb_to_envoy_http_shifted,
  ]
}
