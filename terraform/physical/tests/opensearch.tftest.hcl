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

run "default_off" {
  command = plan
  assert {
    condition     = length(aws_opensearch_domain.this) == 0 && output.opensearch_endpoint == "" && output.opensearch_credential_secret == "" && output.opensearch_domain_arn == ""
    error_message = "domain should be off"
  }
}

run "managed_on" {
  command = plan
  variables { search_backend = "managed" }
  assert {
    condition     = length(aws_opensearch_domain.this) == 1 && length(local.opensearch_subnet_ids) == 3 && local.opensearch_domain_name == "os-dozuki-test"
    error_message = "domain should be on with 3 subnets"
  }
}


run "override_on_count1" {
  command = plan
  variables {
    opensearch_domain_enabled = true
    opensearch_instance_count = 1
  }
  assert {
    condition     = length(aws_opensearch_domain.this) == 1 && length(local.opensearch_subnet_ids) == 1 && local.opensearch_az_count == 1 && aws_opensearch_domain.this[0].cluster_config[0].zone_awareness_enabled == false && length(null_resource.opensearch_slr) == 1
    error_message = "single node"
  }
}

run "count_two" {
  command = plan
  variables {
    search_backend            = "managed"
    opensearch_instance_count = 2
  }
  assert {
    condition     = local.opensearch_az_count == 2 && length(local.opensearch_subnet_ids) == 2
    error_message = "two node"
  }
}

run "count4_fails_divisibility" {
  command = plan
  variables {
    search_backend            = "managed"
    opensearch_instance_count = 4
  }
  expect_failures = [aws_opensearch_domain.this]
}

run "explicit_off_wins" {
  command = plan
  variables {
    search_backend            = "managed"
    opensearch_domain_enabled = false
  }
  assert {
    condition     = length(aws_opensearch_domain.this) == 0 && length(aws_security_group.opensearch) == 0
    error_message = "override off"
  }
}

run "slr_follows_enable_flag" {
  command = plan
  variables { search_backend = "managed" }
  assert {
    condition     = length(null_resource.opensearch_slr) == 1
    error_message = "slr should exist whenever the domain is enabled, independent of account state"
  }
}

run "slr_absent_when_off" {
  command = plan
  assert {
    condition     = length(null_resource.opensearch_slr) == 0
    error_message = "no slr when the domain is off"
  }
}

run "domain_settings" {
  command = plan
  variables { search_backend = "managed" }
  assert {
    condition = (
      length(aws_security_group.opensearch[0].ingress) == 1 &&
      one(aws_security_group.opensearch[0].ingress).from_port == 443 &&
      one(aws_security_group.opensearch[0].ingress).to_port == 443 &&
      one(aws_security_group.opensearch[0].ingress).protocol == "tcp" &&
      length(one(aws_security_group.opensearch[0].ingress).security_groups) == 1 &&
      contains(one(aws_security_group.opensearch[0].ingress).security_groups, module.eks_cluster.cluster_primary_security_group_id)
    )
    error_message = "SG must have one 443 ingress from the cluster primary SG"
  }
  assert {
    condition = (
      aws_opensearch_domain.this[0].advanced_security_options[0].enabled == true &&
      aws_opensearch_domain.this[0].advanced_security_options[0].internal_user_database_enabled == true &&
      aws_opensearch_domain.this[0].encrypt_at_rest[0].enabled == true &&
      aws_opensearch_domain.this[0].node_to_node_encryption[0].enabled == true &&
      aws_opensearch_domain.this[0].domain_endpoint_options[0].enforce_https == true &&
      aws_opensearch_domain.this[0].domain_endpoint_options[0].tls_security_policy == "Policy-Min-TLS-1-2-PFS-2023-10"
    )
    error_message = "FGAC, encryption, HTTPS and TLS policy must all be on"
  }
  assert {
    condition = (
      length(jsondecode(aws_opensearch_domain_policy.this[0].access_policies).Statement) == 1 &&
      jsondecode(aws_opensearch_domain_policy.this[0].access_policies).Statement[0].Principal.AWS == "*" &&
      jsondecode(aws_opensearch_domain_policy.this[0].access_policies).Statement[0].Action == "es:*" &&
      length(jsondecode(aws_opensearch_domain_policy.this[0].access_policies).Statement[0].Resource) == 2
    )
    error_message = "domain policy shape"
  }
}

# The long-identifier hash branch of opensearch_domain_name is unreachable from tests: customer is
# capped at 10 chars and environment at 5, so "os-" plus the identifier is at most 19 chars.

# override_data cannot target one instance of a counted data source, so every mocked subnet
# gets the same AZ. That still exercises the adopted-VPC path and the per-AZ dedupe (three
# subnets in one AZ collapse to the sorted-first id, one AZ). A two-AZ mix is not expressible here.
run "adopted_vpc_dedupes_per_az" {
  command = plan
  variables {
    search_backend            = "managed"
    vpc_id                    = "vpc-0123456789abcdef0"
    opensearch_instance_count = 2
  }
  override_data {
    target = data.aws_vpc.this
    values = { cidr_block = "10.0.0.0/16" }
  }
  override_data {
    target = data.aws_subnets.private
    values = { ids = ["subnet-b2", "subnet-a2", "subnet-c1"] }
  }
  override_data {
    target = data.aws_subnet.opensearch
    values = { availability_zone = "us-east-1a" }
  }
  assert {
    condition     = local.opensearch_az_count == 1 && local.opensearch_subnet_ids[0] == "subnet-a2" && length(local.opensearch_subnet_ids) == 1
    error_message = "adopted VPC: got ${jsonencode(local.opensearch_subnet_ids)}, want sorted-first subnet per AZ, AZ count capped by distinct AZs"
  }
}
