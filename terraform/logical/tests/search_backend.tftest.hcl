# search_backend: in_cluster keeps the OpenSearch subchart and renders nothing new; managed and
# external switch it off and point the chart at Vault. From terraform/logical:
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

run "in_cluster_renders_nothing_new" {
  command = plan
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]))), "search") && nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).opensearch.singleNode == false
    error_message = "in_cluster must render no search key and keep the opensearch scheduling values"
  }
  assert {
    condition     = length(vault_kv_secret_v2.opensearch) == 0
    error_message = "in_cluster must not seed an opensearch Vault entry"
  }
}

run "managed_turns_subchart_off" {
  command = plan
  variables {
    search_backend               = "managed"
    opensearch_endpoint          = "search.example"
    opensearch_credential_secret = "dozuki-sbx-opensearch"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).opensearch == { enabled = false } && nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).search.opensearch.external.enabled == true
    error_message = "managed must set opensearch.enabled=false and search.opensearch.external.enabled=true"
  }
  assert {
    condition     = length(vault_kv_secret_v2.opensearch) == 1
    error_message = "managed must create exactly one opensearch Vault entry"
  }
}

run "external_keeps_reindex_override" {
  command = plan
  variables {
    search_backend         = "external"
    search_reindex_enabled = false
  }
  assert {
    condition     = nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).search.reindex.enabled == false && nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"])).search.opensearch.external.enabled == true && length(vault_kv_secret_v2.opensearch) == 0
    error_message = "external must keep search.reindex, enable external, and seed no Vault entry"
  }
}

run "managed_vault_entry_content" {
  command = plan
  variables {
    customer                     = "acme"
    environment                  = "qa"
    search_backend               = "managed"
    opensearch_endpoint          = "search.example"
    opensearch_credential_secret = "dozuki-sbx-opensearch"
  }
  assert {
    condition     = vault_kv_secret_v2.opensearch[0].mount == "secret" && vault_kv_secret_v2.opensearch[0].name == "acme/qa/opensearch"
    error_message = "managed Vault entry must resolve to <customer>/<environment>/opensearch"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.opensearch[0].data_json)) == { host = "search.example", port = "443", scheme = "https", username = "admin", password = "pw" }
    error_message = "managed Vault entry must carry endpoint, port 443, https and the secret credentials"
  }
}

run "managed_requires_endpoint" {
  command = plan
  variables {
    search_backend               = "managed"
    opensearch_credential_secret = "dozuki-sbx-opensearch"
  }
  expect_failures = [data.aws_secretsmanager_secret_version.opensearch]
}

run "managed_requires_secret" {
  command = plan
  variables {
    search_backend      = "managed"
    opensearch_endpoint = "search.example"
  }
  expect_failures = [data.aws_secretsmanager_secret_version.opensearch]
}

run "managed_requires_aws" {
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
    search_backend               = "managed"
    opensearch_endpoint          = "search.example"
    opensearch_credential_secret = "dozuki-sbx-opensearch"
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "external_requires_aws" {
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
    search_backend               = "external"
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "external_requires_chart_floor" {
  command = plan
  variables {
    search_backend = "external"
    chart_version  = "3.20.0"
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "managed_requires_chart_floor" {
  command = plan
  variables {
    search_backend               = "managed"
    opensearch_endpoint          = "search.example.com"
    opensearch_credential_secret = "search-credentials"
    chart_version                = "3.20.0"
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "managed_rejects_2x_chart" {
  command = plan
  variables {
    search_backend               = "managed"
    opensearch_endpoint          = "search.example.com"
    opensearch_credential_secret = "search-credentials"
    chart_version                = "2.30.0"
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "managed_rejects_malformed_chart" {
  command = plan
  variables {
    search_backend               = "managed"
    opensearch_endpoint          = "search.example.com"
    opensearch_credential_secret = "search-credentials"
    chart_version                = "4"
  }
  expect_failures = [kubernetes_secret_v1.flux_values]
}

run "managed_rejects_scheme_endpoint" {
  command = plan
  variables {
    search_backend               = "managed"
    opensearch_endpoint          = "https://x"
    opensearch_credential_secret = "dozuki-sbx-opensearch"
  }
  expect_failures = [data.aws_secretsmanager_secret_version.opensearch]
}

run "managed_rejects_endpoint_whitespace" {
  command = plan
  variables {
    search_backend               = "managed"
    opensearch_endpoint          = "search.example "
    opensearch_credential_secret = "dozuki-sbx-opensearch"
  }
  expect_failures = [data.aws_secretsmanager_secret_version.opensearch]
}

run "managed_rejects_empty_password" {
  command = plan
  variables {
    search_backend               = "managed"
    opensearch_endpoint          = "search.example"
    opensearch_credential_secret = "dozuki-sbx-opensearch"
  }
  override_data {
    target = data.aws_secretsmanager_secret_version.opensearch
    values = { secret_string = "{\"username\":\"admin\",\"password\":\"\"}" }
  }
  expect_failures = [vault_kv_secret_v2.opensearch]
}
