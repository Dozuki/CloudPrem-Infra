# puppeteer_base_url: renders puppeteer-api's RENDER_BASE_URL through deployments.puppeteer.env, keeps
# the netpol subtree beside it, and is refused where the chart would silently ignore it. From
# terraform/logical:
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
  chart_version              = "3.21.0"
  eks_cluster_id             = "dozuki-sbx"
  primary_db_secret          = "dozuki-sbx-db"
  dns_domain_name            = "env.example.com"
  vault_address              = "https://vault.example:8200"
  nlb_https_target_group_arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/https/0123456789abcdef"
  nlb_http_target_group_arn  = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/http/0123456789abcdef"
  verify_artifact_pins       = false
}


run "unset_renders_no_env" {
  command = plan
  assert {
    condition     = keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).deployments.puppeteer) == ["networkPolicy"]
    error_message = "an unset puppeteer_base_url must leave deployments.puppeteer exactly as master renders it: the networkPolicy key and nothing else"
  }
}

# Below chart 3.20.2 netpol_values is empty, so nothing may emit a puppeteer key: the flux values stay as master renders them.
run "unset_old_chart_emits_no_puppeteer" {
  command = plan
  variables {
    chart_version = "3.20.0"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).deployments), "puppeteer")
    error_message = "an unset puppeteer_base_url on a chart without the netpol values must not emit a puppeteer key"
  }
}

run "set_renders_env_beside_netpol" {
  command = plan
  variables {
    chart_version      = "3.22.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://dozuki.example.com"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).deployments.puppeteer.env == { RENDER_BASE_URL = "https://dozuki.example.com" }
    error_message = "puppeteer_base_url must render as deployments.puppeteer.env.RENDER_BASE_URL"
  }
  assert {
    condition     = contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).deployments.puppeteer), "networkPolicy")
    error_message = "the puppeteer egress deny list must survive next to env"
  }
}

run "set_requires_chart_floor" {
  command = plan
  variables {
    chart_version      = "3.21.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://dozuki.example.com"
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "set_requires_slim" {
  command = plan
  variables {
    chart_version      = "3.22.0"
    puppeteer_base_url = "https://dozuki.example.com"
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "rejects_path" {
  command = plan
  variables {
    chart_version      = "3.22.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://dozuki.example.com/guides"
  }
  expect_failures = [var.puppeteer_base_url]
}

run "rejects_credentials" {
  command = plan
  variables {
    chart_version      = "3.22.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://user:pw@dozuki.example.com"
  }
  expect_failures = [var.puppeteer_base_url]
}

run "set_accepts_a_port" {
  command = plan
  variables {
    chart_version      = "3.22.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://dozuki.example.com:8443"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).deployments.puppeteer.env.RENDER_BASE_URL == "https://dozuki.example.com:8443"
    error_message = "an origin with a port must render as given"
  }
}

run "set_accepts_a_later_major_chart" {
  command = plan
  variables {
    chart_version      = "4.0.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://dozuki.example.com"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).deployments.puppeteer.env.RENDER_BASE_URL == "https://dozuki.example.com"
    error_message = "a chart newer than the floor on a later major must accept the value"
  }
}

run "rejects_backslash" {
  command = plan
  variables {
    chart_version      = "3.22.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://dozuki.example.com\\guides"
  }
  expect_failures = [var.puppeteer_base_url]
}

run "rejects_bad_port" {
  command = plan
  variables {
    chart_version      = "3.22.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://dozuki.example.com:abc"
  }
  expect_failures = [var.puppeteer_base_url]
}

run "rejects_percent_encoding" {
  command = plan
  variables {
    chart_version      = "3.22.0"
    app_image_flavor   = "slim"
    beanstalkd_tag     = "btag"
    puppeteer_base_url = "https://dozuki.example.com%2Fguides"
  }
  expect_failures = [var.puppeteer_base_url]
}
