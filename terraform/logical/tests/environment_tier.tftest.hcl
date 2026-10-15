# environment_tier picks the image registry: prod pulls QA-promoted images from ECR (chart via
# the source-controller pod identity), nonprod pulls CI builds from ghcr.io/dozuki with the
# ghcr_pull_* creds. Azure pulls from GHCR at every tier. Every provider is mocked, so nothing
# reaches a cloud, Vault or a cluster. From terraform/logical:
#   tofu init -backend=false && tofu test
# Mocks cannot see provider configuration, so the helm provider's registry login is not covered.

mock_provider "aws" {
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
  mock_data "aws_region" {
    defaults = { region = "us-east-1", name = "us-east-1" }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "078672473954" }
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
    defaults = { arn = "arn:aws:iam::078672473954:role/mock" }
  }
  mock_data "aws_kms_key" {
    defaults = { arn = "arn:aws:kms:us-east-1:078672473954:key/00000000-0000-0000-0000-000000000000" }
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
  image_repository           = "069174876992.dkr.ecr.us-east-1.amazonaws.com"
  image_tag                  = "2026.9.28.1"
  nextjs_tag                 = "2026.9.24.1"
  chart_version              = "3.18.0"
  eks_cluster_id             = "dozuki-sbx"
  primary_db_secret          = "dozuki-sbx-db"
  dns_domain_name            = "dozuki-sbx.dozuki.cloud"
  vault_address              = "https://vault.example:8200"
  nlb_https_target_group_arn = "arn:aws:elasticloadbalancing:us-east-1:078672473954:targetgroup/https/0123456789abcdef"
  nlb_http_target_group_arn  = "arn:aws:elasticloadbalancing:us-east-1:078672473954:targetgroup/http/0123456789abcdef"
  verify_artifact_pins       = false
}


# ---- tier input ---------------------------------------------------------------------------

run "default_tier_is_prod" {
  command = plan
  assert {
    condition     = var.environment_tier == "prod"
    error_message = "environment_tier must default to prod"
  }
  assert {
    condition     = length(kubernetes_secret_v1.ghcr_pull) == 0 && length(kubernetes_secret_v1.flux_ghcr_pull) == 0
    error_message = "the default tier must behave as prod"
  }
}

run "unknown_tier_fails" {
  command = plan
  variables {
    environment_tier = "staging"
  }
  expect_failures = [var.environment_tier]
}

# ---- prod on AWS: ECR, pod identity, no GHCR wiring ----------------------------------------

run "prod_aws_pulls_ecr" {
  command = plan
  variables {
    environment_tier = "prod"
  }
  assert {
    condition     = length(kubernetes_secret_v1.ghcr_pull) == 0 && length(kubernetes_secret_v1.flux_ghcr_pull) == 0
    error_message = "prod on AWS must not create GHCR pull secrets"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec.provider) == "aws" && !contains(keys(nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec)), "secretRef")
    error_message = "prod OCIRepository must be provider aws with no secretRef"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec.url) == "oci://069174876992.dkr.ecr.us-east-1.amazonaws.com/charts/dozuki"
    error_message = "prod OCIRepository must point at ECR"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).global)), "imagePullSecrets")
    error_message = "prod values must not set global.imagePullSecrets"
  }
  assert {
    condition     = length(data.aws_ecr_authorization_token.chart) == 1
    error_message = "the ECR token the helm registries entry reads must exist on prod AWS"
  }
}

run "prod_aws_gov_pulls_ecr" {
  command = plan
  variables {
    environment_tier = "prod"
    image_repository = "446787640263.dkr.ecr.us-gov-west-1.amazonaws.com"
  }
  override_data {
    target = data.aws_partition.current
    values = { partition = "aws-us-gov" }
  }
  override_data {
    target = data.aws_region.current
    values = { region = "us-gov-west-1", name = "us-gov-west-1" }
  }
  assert {
    condition     = length(kubernetes_secret_v1.ghcr_pull) == 0 && length(kubernetes_secret_v1.flux_ghcr_pull) == 0
    error_message = "prod on gov must not create GHCR pull secrets"
  }
  assert {
    condition     = nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec.provider) == "aws" && !contains(keys(nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec)), "secretRef")
    error_message = "prod gov OCIRepository must be provider aws with no secretRef"
  }
  assert {
    condition     = !contains(keys(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).global)), "imagePullSecrets")
    error_message = "prod gov values must not set global.imagePullSecrets"
  }
}

# ---- nonprod on AWS: GHCR with the ghcr_pull_* creds ---------------------------------------

run "nonprod_aws_pulls_ghcr" {
  command = plan
  variables {
    environment_tier   = "nonprod"
    image_repository   = "ghcr.io/dozuki"
    ghcr_pull_username = "dozuki-bot"
    ghcr_pull_token    = "ghp_mock"
  }
  assert {
    condition = try(
      kubernetes_secret_v1.ghcr_pull[0].metadata[0].name == "ghcr-pull" &&
      kubernetes_secret_v1.ghcr_pull[0].metadata[0].namespace == "dozuki" &&
      kubernetes_secret_v1.flux_ghcr_pull[0].metadata[0].name == "flux-ghcr-pull" &&
      kubernetes_secret_v1.flux_ghcr_pull[0].metadata[0].namespace == "flux-system",
      false
    )
    error_message = "nonprod must create ghcr-pull (app namespace) and flux-ghcr-pull (flux-system)"
  }
  assert {
    condition = try(alltrue([for s in [kubernetes_secret_v1.ghcr_pull[0], kubernetes_secret_v1.flux_ghcr_pull[0]] :
      s.type == "kubernetes.io/dockerconfigjson" &&
      nonsensitive(jsondecode(s.data[".dockerconfigjson"])) == { auths = { "ghcr.io" = { auth = base64encode("dozuki-bot:ghp_mock") } } }
    ]), false)
    error_message = "GHCR secrets must be dockerconfigjson with auths for ghcr.io from ghcr_pull_*"
  }
  assert {
    condition     = try(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).global.imagePullSecrets) == [{ name = "ghcr-pull" }], false)
    error_message = "nonprod values must set global.imagePullSecrets = [ghcr-pull]"
  }
  assert {
    condition     = try(kubernetes_config_map_v1.flux_values_diff.data["global.imagePullSecrets"] == jsonencode([{ name = "ghcr-pull" }]), false)
    error_message = "the values diff view must show global.imagePullSecrets"
  }
  assert {
    condition = nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec) == {
      interval  = "10m"
      url       = "oci://ghcr.io/dozuki/charts/dozuki"
      ref       = { tag = "3.18.0" }
      provider  = "generic"
      secretRef = { name = "flux-ghcr-pull" }
    }
    error_message = "nonprod OCIRepository must be provider generic + secretRef flux-ghcr-pull"
  }
}

# ---- guards: tier must match the registry, nonprod must have creds -------------------------

run "prod_with_ghcr_fails" {
  command = plan
  variables {
    environment_tier = "prod"
    image_repository = "ghcr.io/dozuki"
  }
  expect_failures = [kubectl_manifest.dozuki_ocirepository]
}

run "nonprod_with_ecr_fails" {
  command = plan
  variables {
    environment_tier   = "nonprod"
    ghcr_pull_username = "dozuki-bot"
    ghcr_pull_token    = "ghp_mock"
  }
  expect_failures = [kubectl_manifest.dozuki_ocirepository]
}

run "nonprod_with_lookalike_registry_fails" {
  command = plan
  variables {
    environment_tier   = "nonprod"
    image_repository   = "ghcr.io.example/dozuki"
    ghcr_pull_username = "dozuki-bot"
    ghcr_pull_token    = "ghp_mock"
  }
  expect_failures = [kubectl_manifest.dozuki_ocirepository]
}

run "nonprod_empty_token_fails" {
  command = plan
  variables {
    environment_tier   = "nonprod"
    image_repository   = "ghcr.io/dozuki"
    ghcr_pull_username = "dozuki-bot"
    ghcr_pull_token    = ""
  }
  expect_failures = [kubernetes_secret_v1.ghcr_pull]
}

run "nonprod_empty_username_fails" {
  command = plan
  variables {
    environment_tier   = "nonprod"
    image_repository   = "ghcr.io/dozuki"
    ghcr_pull_username = ""
    ghcr_pull_token    = "ghp_mock"
  }
  expect_failures = [kubernetes_secret_v1.ghcr_pull]
}

# ---- Azure: GHCR at every tier, exempt from both guards ------------------------------------

run "azure_prod_pulls_ghcr" {
  command = plan
  variables {
    cloud                        = "azure"
    environment_tier             = "prod"
    image_repository             = "ghcr.io/dozuki"
    ghcr_pull_username           = "dozuki-bot"
    ghcr_pull_token              = "ghp_mock"
    azure_subscription_id        = "00000000-0000-0000-0000-000000000000"
    azure_resource_group         = "azdev-dev-mpc"
    azure_key_vault_id           = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/azdev-dev-mpc/providers/Microsoft.KeyVault/vaults/kv-azdevdev-abcd"
    azure_key_vault_uri          = "https://kv-azdevdev-abcd.vault.azure.net/"
    azure_tenant_id              = "00000000-0000-0000-0000-000000000000"
    azure_eso_identity_client_id = "00000000-0000-0000-0000-000000000000"
  }
  assert {
    condition     = length(kubernetes_secret_v1.ghcr_pull) == 1 && length(kubernetes_secret_v1.flux_ghcr_pull) == 1
    error_message = "azure must create both GHCR pull secrets"
  }
  assert {
    condition     = try(nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec.provider) == "generic" && nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec.secretRef) == { name = "flux-ghcr-pull" }, false)
    error_message = "azure OCIRepository must be provider generic + secretRef flux-ghcr-pull"
  }
  assert {
    condition     = try(nonsensitive(yamldecode(kubernetes_secret_v1.flux_values.data["values.yaml"]).global.imagePullSecrets) == [{ name = "ghcr-pull" }], false)
    error_message = "azure values must set global.imagePullSecrets = [ghcr-pull]"
  }
}

run "azure_nonprod_pulls_ghcr" {
  command = plan
  variables {
    cloud                        = "azure"
    environment_tier             = "nonprod"
    image_repository             = "ghcr.io/dozuki"
    ghcr_pull_username           = "dozuki-bot"
    ghcr_pull_token              = "ghp_mock"
    azure_subscription_id        = "00000000-0000-0000-0000-000000000000"
    azure_resource_group         = "azdev-dev-mpc"
    azure_key_vault_id           = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/azdev-dev-mpc/providers/Microsoft.KeyVault/vaults/kv-azdevdev-abcd"
    azure_key_vault_uri          = "https://kv-azdevdev-abcd.vault.azure.net/"
    azure_tenant_id              = "00000000-0000-0000-0000-000000000000"
    azure_eso_identity_client_id = "00000000-0000-0000-0000-000000000000"
  }
  assert {
    condition     = try(nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec.provider) == "generic" && nonsensitive(yamldecode(kubectl_manifest.dozuki_ocirepository.yaml_body).spec.secretRef) == { name = "flux-ghcr-pull" }, false)
    error_message = "azure nonprod OCIRepository must be provider generic + secretRef flux-ghcr-pull"
  }
}

run "azure_nonprod_with_ecr_passes" {
  command = plan
  variables {
    cloud                        = "azure"
    environment_tier             = "nonprod"
    image_repository             = "069174876992.dkr.ecr.us-east-1.amazonaws.com"
    ghcr_pull_username           = "dozuki-bot"
    ghcr_pull_token              = "ghp_mock"
    azure_subscription_id        = "00000000-0000-0000-0000-000000000000"
    azure_resource_group         = "azdev-dev-mpc"
    azure_key_vault_id           = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/azdev-dev-mpc/providers/Microsoft.KeyVault/vaults/kv-azdevdev-abcd"
    azure_key_vault_uri          = "https://kv-azdevdev-abcd.vault.azure.net/"
    azure_tenant_id              = "00000000-0000-0000-0000-000000000000"
    azure_eso_identity_client_id = "00000000-0000-0000-0000-000000000000"
  }
}

run "azure_empty_token_passes" {
  command = plan
  variables {
    cloud                        = "azure"
    image_repository             = "ghcr.io/dozuki"
    ghcr_pull_token              = ""
    azure_subscription_id        = "00000000-0000-0000-0000-000000000000"
    azure_resource_group         = "azdev-dev-mpc"
    azure_key_vault_id           = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/azdev-dev-mpc/providers/Microsoft.KeyVault/vaults/kv-azdevdev-abcd"
    azure_key_vault_uri          = "https://kv-azdevdev-abcd.vault.azure.net/"
    azure_tenant_id              = "00000000-0000-0000-0000-000000000000"
    azure_eso_identity_client_id = "00000000-0000-0000-0000-000000000000"
  }
  assert {
    condition     = length(kubernetes_secret_v1.ghcr_pull) == 1
    error_message = "azure with an empty token must still plan the ghcr-pull secret"
  }
}
