# dns_validation input: null keeps the legacy rule, true/false override it, and the cert-manager
# chart keys only appear for DNS-01. Providers are mocked. From terraform/logical:
#   tofu init -backend=false && tofu test

mock_provider "aws" {
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
  mock_data "aws_region" {
    defaults = { region = "us-east-1", name = "us-east-1" }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012" }
  }
  mock_data "aws_default_tags" {
    defaults = { tags = { Service = "dozuki", Environment = "sbx" } }
  }
  mock_data "aws_eks_cluster" {
    defaults = {
      endpoint              = "https://ABC.gr7.us-east-1.eks.amazonaws.com"
      certificate_authority = [{ data = "Y2E=" }]
      vpc_config = [{
        vpc_id                    = "vpc-0123456789abcdef0"
        cluster_security_group_id = "sg-0"
        control_plane_egress_mode = null
        endpoint_private_access   = true
        endpoint_public_access    = true
        public_access_cidrs       = []
        security_group_ids        = []
        subnet_ids                = []
      }]
      kubernetes_network_config = [{
        service_ipv4_cidr      = "172.20.0.0/16"
        service_ipv6_cidr      = ""
        ip_family              = "ipv4"
        elastic_load_balancing = []
      }]
    }
  }
  mock_data "aws_vpc" {
    defaults = { cidr_block_associations = [{ cidr_block = "10.0.0.0/16", association_id = "vpc-cidr-assoc-0", state = "associated" }] }
  }
  mock_data "aws_ecr_authorization_token" {
    defaults = { user_name = "AWS", password = "ecr-token" }
  }
  mock_data "aws_secretsmanager_secret_version" {
    defaults = { secret_string = "{\"host\":\"db.example\",\"username\":\"admin\",\"password\":\"pw\"}" }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{}" }
  }
  mock_resource "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::123456789012:role/mock" }
  }
  mock_data "aws_kms_key" {
    defaults = { arn = "arn:aws:kms:us-east-1:123456789012:key/00000000-0000-0000-0000-000000000000" }
  }
}

mock_provider "azurerm" {
  alias    = "main"
  for_each = toset(["azure"])
  mock_data "azurerm_kubernetes_cluster" {
    defaults = { kube_config = [{
      host                   = "https://aks.example"
      cluster_ca_certificate = "Y2E="
      client_certificate     = ""
      client_key             = ""
      password               = ""
      username               = ""
      }]
    location = "eastus" }
  }
  mock_data "azurerm_key_vault_secret" {
    defaults = { value = "{\"host\":\"db.example\",\"username\":\"admin\",\"password\":\"pw\"}" }
  }
}

mock_provider "kubernetes" {
  mock_data "kubernetes_resource" {
    defaults = {
      object = {
        spec = {
          role                       = "AmazonEKSAutoNodeRole"
          subnetSelectorTerms        = [{ id = "subnet-0" }]
          securityGroupSelectorTerms = [{ id = "sg-0" }]
        }
      }
    }
  }
}
mock_provider "helm" {}

override_resource {
  target = kubernetes_secret_v1.vault_auth_token
  values = { data = { token = "jwt", "ca.crt" = "ca" } }
}
mock_provider "kubectl" {}
mock_provider "tls" {}
mock_provider "null" {}
mock_provider "local" {}
mock_provider "random" {
  mock_resource "random_password" {
    defaults = { result = "mock-random-password" }
  }
}
mock_provider "vault" {
  mock_data "vault_kv_secret_v2" {
    defaults = { data = { "api-key" = "x" } }
  }
}
mock_provider "external" {
  mock_data "external" {
    defaults = { result = { hash = "aGFzaA==" } }
  }
}

# Minimal valid AWS stack (commercial, prod by default). Runs override only what they test.
variables {
  cloud                      = "aws"
  image_repository           = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
  image_tag                  = "2026.9.28.1"
  nextjs_tag                 = "2026.9.24.1"
  chart_version              = "3.18.0"
  eks_cluster_id             = "dozuki-sbx"
  primary_db_secret          = "dozuki-sbx-db"
  dns_domain_name            = "dozuki-sbx.dozuki.cloud"
  vault_address              = "https://vault.example:8200"
  nlb_https_target_group_arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/https/0123456789abcdef"
  nlb_http_target_group_arn  = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/http/0123456789abcdef"
  verify_artifact_pins       = false
}


run "null_legacy_dozuki_name" {
  command = plan
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).dns_validation) == true
    error_message = "null keeps the legacy rule: a dozuki.cloud name gets DNS-01"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]))), "cert_manager") && !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).gateway)), "wildcardCertFromIssuer")
    error_message = "no DNS-01 chart keys by default"
  }
}

run "null_legacy_foreign_name" {
  command = plan
  variables {
    dns_domain_name = "wiki.customer.example.com"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).dns_validation) == false
    error_message = "null keeps the legacy rule: a foreign name gets HTTP-01"
  }
}

run "true_on_foreign_name" {
  command = plan
  variables {
    dns_domain_name = "wiki.customer.example.com"
    dns_validation  = true
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).dns_validation) == true
    error_message = "true forces DNS-01"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]))), "cert_manager") && !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).gateway)), "wildcardCertFromIssuer")
    error_message = "no chart keys without an own-account zone or a wildcard host"
  }
}

run "false_on_dozuki_name" {
  command = plan
  variables {
    dns_validation = false
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).dns_validation) == false
    error_message = "false forces HTTP-01"
  }
}

run "true_with_manual_tls" {
  command = plan
  variables {
    dns_validation                    = true
    customer_tls_externally_managed   = true
    external_fqdn_zone_in_env_account = true
    external_fqdn_route53_zone_id     = "Z0EXAMPLE0000"
    ingress_hostname                  = "*.customer.example.com"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).dns_validation) == false
    error_message = "manual TLS always turns DNS-01 off"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]))), "cert_manager") && !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).gateway)), "wildcardCertFromIssuer")
    error_message = "no chart keys with manual TLS"
  }
}

run "true_on_gov_rejected" {
  command = plan
  variables {
    dns_validation = true
  }
  override_data {
    target = data.aws_partition.current
    values = { partition = "aws-us-gov" }
  }
  override_data {
    target = data.aws_region.current
    values = { region = "us-gov-west-1", name = "us-gov-west-1" }
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "own_zone_sets_route53" {
  command = plan
  variables {
    dns_validation                    = true
    external_fqdn_zone_in_env_account = true
    external_fqdn_route53_zone_id     = "Z0EXAMPLE0000"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).cert_manager) == { route53 = { role = "", hostedZoneID = "Z0EXAMPLE0000", dnsZones = [] } }
    error_message = "own-account zone sets cert_manager.route53"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).gateway)), "wildcardCertFromIssuer")
    error_message = "non-wildcard host must not set wildcardCertFromIssuer"
  }
}

run "wildcard_host_sets_issuer_flag" {
  command = plan
  variables {
    dns_validation   = true
    ingress_hostname = "*.customer.example.com"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).gateway.wildcardCertFromIssuer) == true
    error_message = "wildcard primary host sets wildcardCertFromIssuer"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]))), "cert_manager")
    error_message = "no cert_manager key without an own-account zone"
  }
}
