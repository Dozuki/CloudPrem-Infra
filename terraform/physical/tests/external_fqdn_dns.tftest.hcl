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
  mock_resource "aws_lb" { defaults = { dns_name = "stub-nlb.elb.us-east-1.amazonaws.com", arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/net/stub/0123456789abcdef" } }
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
    condition     = length(aws_route53_record.external_fqdn) == 0 && length(aws_route53_record.external_fqdn_wildcard) == 0
    error_message = "no external_fqdn records by default"
  }
}

run "zone_set" {
  command = plan
  variables {
    external_fqdn                 = "ext.example.com"
    external_fqdn_route53_zone_id = "Z0123456789"
  }
  assert {
    condition = (
      length(aws_route53_record.external_fqdn) == 1 &&
      length(aws_route53_record.external_fqdn_wildcard) == 1 &&
      aws_route53_record.external_fqdn[0].name == "ext.example.com" &&
      aws_route53_record.external_fqdn_wildcard[0].name == "*.ext.example.com" &&
      aws_route53_record.external_fqdn[0].zone_id == "Z0123456789" &&
      aws_route53_record.external_fqdn_wildcard[0].zone_id == "Z0123456789" &&
      one(aws_route53_record.external_fqdn[0].records) == "stub-nlb.elb.us-east-1.amazonaws.com" &&
      one(aws_route53_record.external_fqdn_wildcard[0].records) == "stub-nlb.elb.us-east-1.amazonaws.com" &&
      aws_route53_record.external_fqdn[0].type == "CNAME" &&
      aws_route53_record.external_fqdn_wildcard[0].type == "CNAME"
    )
    error_message = "both CNAMEs should be planned"
  }
}

run "fqdn_without_zone" {
  command = plan
  variables {
    external_fqdn = "ext.example.com"
  }
  assert {
    condition     = length(aws_route53_record.external_fqdn) == 0 && length(aws_route53_record.external_fqdn_wildcard) == 0
    error_message = "no records without a zone id"
  }
}

run "zone_without_fqdn" {
  command = plan
  variables {
    external_fqdn_route53_zone_id = "Z0123456789"
  }
  expect_failures = [var.external_fqdn_route53_zone_id]
}

run "gov_skips_records" {
  command = plan
  override_data {
    target = data.aws_partition.current
    values = { partition = "aws-us-gov", dns_suffix = "amazonaws.com" }
  }
  variables {
    external_fqdn                 = "ext.example.com"
    external_fqdn_route53_zone_id = "Z0123456789"
  }
  assert {
    condition     = length(aws_route53_record.external_fqdn) == 0 && length(aws_route53_record.external_fqdn_wildcard) == 0
    error_message = "gov creates no records"
  }
}

run "local_zone_flag" {
  command = plan
  variables {
    external_fqdn                     = "ext.example.com"
    external_fqdn_route53_zone_id     = "Z0123456789"
    external_fqdn_zone_in_env_account = true
  }
  assert {
    condition = (
      length(aws_route53_record.external_fqdn) == 0 &&
      length(aws_route53_record.external_fqdn_wildcard) == 0 &&
      length(aws_route53_record.external_fqdn_local) == 1 &&
      length(aws_route53_record.external_fqdn_local_wildcard) == 1 &&
      aws_route53_record.external_fqdn_local[0].name == "ext.example.com" &&
      aws_route53_record.external_fqdn_local_wildcard[0].name == "*.ext.example.com" &&
      aws_route53_record.external_fqdn_local[0].zone_id == "Z0123456789" &&
      aws_route53_record.external_fqdn_local_wildcard[0].zone_id == "Z0123456789" &&
      one(aws_route53_record.external_fqdn_local[0].records) == "stub-nlb.elb.us-east-1.amazonaws.com" &&
      one(aws_route53_record.external_fqdn_local_wildcard[0].records) == "stub-nlb.elb.us-east-1.amazonaws.com"
    )
    error_message = "only the local pair should be planned"
  }
}

run "dns_pair_when_flag_false" {
  command = plan
  variables {
    external_fqdn                 = "ext.example.com"
    external_fqdn_route53_zone_id = "Z0123456789"
  }
  assert {
    condition     = length(aws_route53_record.external_fqdn) == 1 && length(aws_route53_record.external_fqdn_wildcard) == 1 && length(aws_route53_record.external_fqdn_local) == 0 && length(aws_route53_record.external_fqdn_local_wildcard) == 0
    error_message = "only the dns pair should be planned"
  }
}

run "local_flag_without_zone" {
  command = plan
  variables {
    external_fqdn                     = "ext.example.com"
    external_fqdn_zone_in_env_account = true
  }
  expect_failures = [var.external_fqdn_zone_in_env_account]
}

run "gov_local_pair" {
  command = plan
  override_data {
    target = data.aws_partition.current
    values = { partition = "aws-us-gov", dns_suffix = "amazonaws.com" }
  }
  variables {
    external_fqdn                     = "ext.example.com"
    external_fqdn_route53_zone_id     = "Z0123456789"
    external_fqdn_zone_in_env_account = true
  }
  assert {
    condition     = length(aws_route53_record.external_fqdn_local) == 1 && length(aws_route53_record.external_fqdn_local_wildcard) == 1 && length(aws_route53_record.external_fqdn) == 0 && length(aws_route53_record.external_fqdn_wildcard) == 0
    error_message = "gov still gets the local pair"
  }
}

run "cert_manager_dns01_policy_absent_by_default" {
  command = plan
  assert {
    condition     = length(aws_iam_policy.cert_manager_dns01) == 0 && length(aws_iam_role_policy_attachment.cert_manager_dns01) == 0
    error_message = "no DNS-01 policy without an in-account zone"
  }
}

run "cert_manager_dns01_policy_absent_for_foreign_zone" {
  command = plan
  variables {
    external_fqdn                 = "ext.example.com"
    external_fqdn_route53_zone_id = "Z0EXAMPLE0000"
  }
  assert {
    condition     = length(aws_iam_policy.cert_manager_dns01) == 0
    error_message = "no DNS-01 policy when the zone is not in this account"
  }
}

run "cert_manager_dns01_policy_present" {
  command = plan
  variables {
    external_fqdn                     = "ext.example.com"
    external_fqdn_route53_zone_id     = "Z0EXAMPLE0000"
    external_fqdn_zone_in_env_account = true
  }
  assert {
    condition     = length(aws_iam_role_policy_attachment.cert_manager_dns01) == 1
    error_message = "the DNS-01 policy should be attached"
  }
  assert {
    condition = jsondecode(aws_iam_policy.cert_manager_dns01[0].policy).Statement == [
      {
        Effect   = "Allow"
        Action   = ["route53:GetChange"]
        Resource = ["arn:aws:route53:::change/*"]
      },
      {
        Effect   = "Allow"
        Action   = ["route53:ListResourceRecordSets"]
        Resource = ["arn:aws:route53:::hostedzone/Z0EXAMPLE0000"]
      },
      {
        Effect   = "Allow"
        Action   = ["route53:ChangeResourceRecordSets"]
        Resource = ["arn:aws:route53:::hostedzone/Z0EXAMPLE0000"]
        Condition = {
          "ForAllValues:StringEquals" = {
            "route53:ChangeResourceRecordSetsRecordTypes" = ["TXT"]
            "route53:ChangeResourceRecordSetsActions"     = ["UPSERT", "DELETE"]
          }
          "ForAllValues:StringLike" = {
            "route53:ChangeResourceRecordSetsNormalizedRecordNames" = ["_acme-challenge.*"]
          }
        }
      },
    ]
    error_message = "the DNS-01 policy must be exactly the scoped TXT statements"
  }
}
