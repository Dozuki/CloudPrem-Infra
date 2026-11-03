# ecp_config: optional ECP secret blobs land in Vault and flip chart flags; unset adds nothing. From terraform/logical:
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

run "unset_renders_nothing" {
  command = plan
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]))), "ecpConfig")
    error_message = "no ecp vars must not add ecpConfig to the chart values"
  }
  assert {
    condition     = length(vault_kv_secret_v2.ecp_frontegg) + length(vault_kv_secret_v2.ecp_sentry) + length(vault_kv_secret_v2.ecp_mail) + length(vault_kv_secret_v2.ecp_sendgrid) + length(vault_kv_secret_v2.ecp_openai) + length(vault_kv_secret_v2.ecp_pendo) + length(vault_kv_secret_v2.ecp_slack) + length(vault_kv_secret_v2.ecp_web_nextjs) == 0
    error_message = "no ecp vars must create no ecp Vault entries"
  }
}

run "set_flags_and_vault_entries" {
  command = plan
  variables {
    customer                = "acme"
    environment             = "qa"
    ecp_frontegg_json       = "{\"clientId\":\"fake-token\"}"
    ecp_web_nextjs_env_json = "{\"FOO\":\"fake-token\",\"N\":3}"
    ecp_cdn                 = { host = "cdn.example" }
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.eso.frontegg == true && nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.eso.webNextjsEnv == true && nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.eso.mail == false
    error_message = "eso flags must be true only for the set blobs"
  }
  assert {
    condition     = keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.passthrough) == ["cdn"] && nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.passthrough.cdn.host == "cdn.example"
    error_message = "passthrough must carry only the set keys"
  }
  assert {
    condition     = !strcontains(kubernetes_secret_v1.flux_values.data["values.yaml"], "fake-token")
    error_message = "blob values must never enter the chart values"
  }
  assert {
    condition     = length(vault_kv_secret_v2.ecp_frontegg) == 1 && vault_kv_secret_v2.ecp_frontegg[0].name == "acme/qa/frontegg" && nonsensitive(jsondecode(vault_kv_secret_v2.ecp_frontegg[0].data_json)) == { json = "{\"clientId\":\"fake-token\"}" }
    error_message = "frontegg entry must hold the blob under property json"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.ecp_web_nextjs[0].data_json)) == { FOO = "fake-token", N = 3 } && length(vault_kv_secret_v2.ecp_mail) == 0
    error_message = "web-nextjs entry must be the input object and unset blobs must create nothing"
  }
}

run "rejects_json_array" {
  command = plan
  variables {
    ecp_frontegg_json = "[1,2]"
  }
  expect_failures = [var.ecp_frontegg_json]
}

run "rejects_json_string" {
  command = plan
  variables {
    ecp_frontegg_json = "\"just-a-string\""
  }
  expect_failures = [var.ecp_frontegg_json]
}

run "azure_creates_no_ecp_entries" {
  command = plan
  variables {
    cloud                        = "azure"
    image_repository             = "ghcr.io/dozuki"
    ghcr_pull_username           = "dozuki-bot"
    ghcr_pull_token              = "ghp_mock"
    azure_subscription_id        = "00000000-0000-0000-0000-000000000000"
    azure_resource_group         = "azdev-dev-mpc"
    azure_key_vault_id           = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/azdev-dev-mpc/providers/Microsoft.KeyVault/vaults/kv-azdevdev-abcd"
    azure_key_vault_uri          = "https://kv-azdevdev-abcd.vault.azure.net/"
    azure_tenant_id              = "00000000-0000-0000-0000-000000000000"
    azure_eso_identity_client_id = "00000000-0000-0000-0000-000000000000"
    ecp_frontegg_json            = "{\"clientId\":\"fake-token\"}"
  }
  assert {
    condition     = length(vault_kv_secret_v2.ecp_frontegg) == 0
    error_message = "azure must create no ecp Vault entries"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]))), "ecpConfig")
    error_message = "a secret ecp var on azure must not change the chart values"
  }
}

run "sentry_set" {
  command = plan
  variables {
    customer        = "acme"
    environment     = "qa"
    ecp_sentry_json = "{\"dsn\":\"fake-token\"}"
  }
  assert {
    condition     = length(vault_kv_secret_v2.ecp_sentry) == 1 && vault_kv_secret_v2.ecp_sentry[0].name == "acme/qa/sentry" && nonsensitive(jsondecode(vault_kv_secret_v2.ecp_sentry[0].data_json)) == { json = "{\"dsn\":\"fake-token\"}" }
    error_message = "sentry entry must hold the blob under property json at <customer>/<environment>/sentry"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.eso.sentryOverride == true
    error_message = "sentryOverride must be true"
  }
}

run "all_secrets_set" {
  command = plan
  variables {
    customer                = "acme"
    environment             = "qa"
    ecp_sentry_json         = "{\"k\":\"SENTINEL-do-not-leak\"}"
    ecp_frontegg_json       = "{\"k\":\"SENTINEL-do-not-leak\"}"
    ecp_mail_json           = "{\"k\":\"SENTINEL-do-not-leak\"}"
    ecp_sendgrid_json       = "{\"k\":\"SENTINEL-do-not-leak\"}"
    ecp_openai_json         = "{\"k\":\"SENTINEL-do-not-leak\"}"
    ecp_pendo_json          = "{\"k\":\"SENTINEL-do-not-leak\"}"
    ecp_slack_json          = "{\"k\":\"SENTINEL-do-not-leak\"}"
    ecp_web_nextjs_env_json = "{\"FOO\":\"SENTINEL-do-not-leak\"}"
  }
  assert {
    condition     = alltrue([for r in [vault_kv_secret_v2.ecp_sentry, vault_kv_secret_v2.ecp_frontegg, vault_kv_secret_v2.ecp_mail, vault_kv_secret_v2.ecp_sendgrid, vault_kv_secret_v2.ecp_openai, vault_kv_secret_v2.ecp_pendo, vault_kv_secret_v2.ecp_slack, vault_kv_secret_v2.ecp_web_nextjs] : length(r) == 1])
    error_message = "every secret var must create one Vault entry"
  }
  assert {
    condition     = [for r in [vault_kv_secret_v2.ecp_sentry, vault_kv_secret_v2.ecp_frontegg, vault_kv_secret_v2.ecp_mail, vault_kv_secret_v2.ecp_sendgrid, vault_kv_secret_v2.ecp_openai, vault_kv_secret_v2.ecp_pendo, vault_kv_secret_v2.ecp_slack, vault_kv_secret_v2.ecp_web_nextjs] : r[0].name] == [for p in ["sentry", "frontegg", "mail", "sendgrid", "openai", "pendo", "slack", "web-nextjs"] : "acme/qa/${p}"]
    error_message = "Vault paths must be <customer>/<environment>/<domain>"
  }
  assert {
    condition     = alltrue(values(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.eso))
    error_message = "every eso flag must be true"
  }
  assert {
    condition     = !strcontains(kubernetes_secret_v1.flux_values.data["values.yaml"], "SENTINEL-do-not-leak") && !strcontains(nonsensitive(jsonencode(kubernetes_config_map_v1.flux_values_diff.data)), "SENTINEL-do-not-leak")
    error_message = "blob contents must not appear in the values secret or the diff ConfigMap"
  }
}

run "passthrough_only" {
  command = plan
  variables {
    ecp_csp = { default-src = "self" }
  }
  assert {
    condition     = !anytrue(values(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.eso)) && keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).ecpConfig.passthrough) == ["csp"]
    error_message = "passthrough only must render ecpConfig with every eso flag false"
  }
  assert {
    condition     = length(vault_kv_secret_v2.ecp_frontegg) + length(vault_kv_secret_v2.ecp_web_nextjs) + length(vault_kv_secret_v2.ecp_sentry) == 0
    error_message = "passthrough only must create no Vault entries"
  }
}

run "frontegg_rejects_invalid_json" {
  command = plan
  variables {
    ecp_frontegg_json = "{not json"
  }
  expect_failures = [var.ecp_frontegg_json]
}

run "web_nextjs_rejects_nested_object" {
  command = plan
  variables {
    ecp_web_nextjs_env_json = "{\"a\":{\"b\":1}}"
  }
  expect_failures = [var.ecp_web_nextjs_env_json]
}

run "web_nextjs_rejects_null_value" {
  command = plan
  variables {
    ecp_web_nextjs_env_json = "{\"a\":null}"
  }
  expect_failures = [var.ecp_web_nextjs_env_json]
}

run "web_nextjs_rejects_bad_key" {
  command = plan
  variables {
    ecp_web_nextjs_env_json = "{\"a b\":\"x\"}"
  }
  expect_failures = [var.ecp_web_nextjs_env_json]
}

run "web_nextjs_rejects_invalid_json" {
  command = plan
  variables {
    ecp_web_nextjs_env_json = "{nope"
  }
  expect_failures = [var.ecp_web_nextjs_env_json]
}

run "rejects_passthrough_non_object" {
  command = plan
  variables {
    ecp_cdn = ["x"]
  }
  expect_failures = [var.ecp_cdn]
}
