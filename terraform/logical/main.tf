terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 3.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
    vault = {
      source  = "hashicorp/vault"
      version = "~> 4.0"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.0"
    }
    # external — only for data.external.ops_htpasswd_hash (vault.tf): OpenTofu/
    # Terraform have no base64sha1() (only base64sha256/512), and Envoy Gateway's
    # basic_auth filter only accepts that SHA1 htpasswd format, so we shell out to
    # openssl. First-party HashiCorp provider, same trust tier as null/local/tls.
    external = {
      source  = "hashicorp/external"
      version = "~> 2.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
    # kubectl provider — used ONLY to server-side-apply the Envoy Gateway CRDs that
    # Helm cannot upgrade (see envoy_gateway_crds.tf). It applies raw manifests via
    # the Kubernetes API (client-go); it does NOT shell out to a kubectl binary, so
    # nothing extra is needed on the Spacelift/CI runners.
    #
    # Why a third-party provider (intentional — please don't "fix" this to a
    # first-party one):
    #   - hashicorp/kubernetes' `kubernetes_manifest` requires the CRD schema to
    #     exist at PLAN time (forces a two-apply workflow) and deep-diffs the whole
    #     object, which chokes on the oversized Gateway API CRDs. Unusable here.
    #   - The original gavinbunney/kubectl is abandoned (no Plugin Framework, no
    #     server-side apply).
    #   - alekc/kubectl is the actively-maintained community fork everyone migrated
    #     to (Plugin Framework + server-side apply + OpenTofu support); it's the
    #     de-facto standard for raw-manifest management. HashiCorp never shipped a
    #     first-party kubectl provider.
    # Pinned EXACT (third-party supply chain) and scoped to the EG CRDs only.
    kubectl = {
      source  = "alekc/kubectl"
      version = "2.1.5"
    }
  }
}

provider "kubernetes" {
  host                   = local.cluster_host
  cluster_ca_certificate = base64decode(local.cluster_ca)

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = local.k8s_exec_command
    args        = local.k8s_exec_args
  }
}

provider "kubectl" {
  host                   = local.cluster_host
  cluster_ca_certificate = base64decode(local.cluster_ca)
  load_config_file       = false

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = local.k8s_exec_command
    args        = local.k8s_exec_args
  }
}

provider "helm" {
  # helm provider 3.x (plugin framework): nested config is attribute syntax.
  kubernetes = {
    host                   = local.cluster_host
    cluster_ca_certificate = base64decode(local.cluster_ca)

    exec = {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = local.k8s_exec_command
      args        = local.k8s_exec_args
    }
  }

  # OCI chart-pull auth (helm provider 3.x: `registries` is a list attribute,
  # replacing 2.x's repeatable `registry {}` block). AWS authenticates
  # in-Terraform via the ECR token. Azure authenticates to GHCR with the
  # ghcr_pull_* creds (same ones used for the cluster image-pull secret) — the
  # kit does an ambient `helm registry login`, but a Spacelift worker has no
  # such login, so the provider must carry the creds itself.
  registries = (
    var.cloud == "aws" ? [{
      url      = "oci://${var.image_repository}"
      username = data.aws_ecr_authorization_token.chart[0].user_name
      password = data.aws_ecr_authorization_token.chart[0].password
    }] :
    var.ghcr_pull_token != "" ? [{
      url      = "oci://${var.image_repository}"
      username = var.ghcr_pull_username
      password = var.ghcr_pull_token
    }] : []
  )
}

provider "vault" {
  # Address uses VAULT_ADDR env var: Spacelift sets this to the public NLB,
  # local runs use port-forward. var.vault_address (PrivateLink) is passed
  # to the helm chart separately for in-cluster access.
  skip_child_token = true

  # In Spacelift, authenticate via AWS IAM (deployer role in vault-infrastructure).
  # Uses the generic auth_login with method=aws to work around a known bug in
  # auth_login_aws (hashicorp/terraform-provider-vault#1655) where v4.x requires
  # explicit HCL credentials instead of reading AWS env vars.
  # Locally, fall back to VAULT_TOKEN env var (no auth_login block).
  dynamic "auth_login" {
    for_each = var.spacelift ? [1] : []
    content {
      path   = "auth/aws/login"
      method = "aws"
      # On GovCloud the AWS-auth login must sign sts:GetCallerIdentity against the
      # gov regional STS endpoint. The SDK/env default to the commercial global
      # endpoint (sts.amazonaws.com), which rejects gov credentials with
      # "Credential should be scoped to a valid region" — even with AWS_REGION /
      # AWS_STS_REGIONAL_ENDPOINTS set, the generic auth_login doesn't pick them up.
      # aws_region + aws_sts_endpoint in parameters ARE consumed by the provider's
      # AWS signing. (Assumes us-gov-west-1, the only gov region in use.)
      parameters = merge(
        { role = "deployer" },
        local.is_us_gov ? {
          aws_region       = "us-gov-west-1"
          aws_sts_endpoint = "https://sts.us-gov-west-1.amazonaws.com"
        } : {}
      )
    }
  }
}

# azurerm is configured per-instance via OpenTofu provider for_each: exactly one
# instance on Azure, ZERO on AWS. With an empty set the provider is never
# configured and never authenticates — the only clean fix, because count=0 on the
# azure resources does NOT stop Terraform/OpenTofu from configuring the provider,
# and azurerm v4 authenticates eagerly at configure (no offline mode). Requires
# OpenTofu >= 1.9 (provider for_each); the logical layer runs on OpenTofu.
provider "azurerm" {
  alias    = "main"
  for_each = local.azure_instances

  subscription_id = var.azure_subscription_id == "" ? null : var.azure_subscription_id
  environment     = var.azure_environment

  features {}
}

locals {
  # Provider-instance set for the azurerm provider for_each above: one on Azure,
  # none on AWS (so azurerm is never configured/authenticated on AWS deploys).
  azure_instances = var.cloud == "azure" ? toset(["azure"]) : toset([])

  is_us_gov = var.cloud == "aws" ? data.aws_partition.current[0].partition == "aws-us-gov" : false
  ca_cert_pem_file = var.cloud == "azure" ? "vendor/azure-mysql-global.pem" : (
    local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/global-bundle.pem"
  )

  # Cluster auth (cloud-conditional)
  cluster_host = var.cloud == "aws" ? data.aws_eks_cluster.main[0].endpoint : data.azurerm_kubernetes_cluster.main["azure"].kube_config[0].host
  cluster_ca   = var.cloud == "aws" ? data.aws_eks_cluster.main[0].certificate_authority[0].data : data.azurerm_kubernetes_cluster.main["azure"].kube_config[0].cluster_ca_certificate

  k8s_exec_command = var.cloud == "aws" ? "aws" : "kubelogin"
  k8s_exec_args = var.cloud == "aws" ? [
    "eks", "get-token", "--cluster-name", var.eks_cluster_id, "--region", data.aws_region.current[0].region
    ] : concat(
    ["get-token", "--server-id", "6dae42f8-4368-4678-94ff-3960e28e3630", "--login", var.azure_kubelogin_login],
    var.azure_environment == "usgovernment" ? ["--environment", "AzureUSGovernmentCloud"] : []
  )

  # Database
  db_credentials = var.cloud == "aws" ? jsondecode(data.aws_secretsmanager_secret_version.db_master[0].secret_string) : jsondecode(data.azurerm_key_vault_secret.db_master["azure"].value)

  db_master_host     = nonsensitive(local.db_credentials["host"])
  db_master_username = nonsensitive(local.db_credentials["username"])
  db_master_password = local.db_credentials["password"]

  db_bi_host     = var.enable_bi ? nonsensitive(jsondecode(data.aws_secretsmanager_secret_version.db_bi[0].secret_string)["host"]) : ""
  db_bi_password = var.enable_bi ? jsondecode(data.aws_secretsmanager_secret_version.db_bi[0].secret_string)["password"] : ""

  # Grafana
  grafana_url            = var.enable_bi ? format("https://%s/%s", var.dns_domain_name, var.grafana_subpath) : null
  grafana_admin_username = var.enable_bi ? var.customer != "" ? var.customer : "dozuki" : "dozuki"
  grafana_admin_password = var.enable_bi ? random_password.grafana_admin[0].result : ""

  # Kubernetes
  k8s_namespace_name = "dozuki"

  # Dashboards (shared Grafana): jwt signing secret + admin creds, generated once
  # per stack. Shared between the "grafana" Vault/Key Vault secret (vault.tf /
  # keyvault.tf, read by ESO) and the dashboards.jwtSecret chart value (kubernetes.tf),
  # which the Envoy Gateway JWT SecurityPolicy needs baked in at render time — a
  # cluster secret alone isn't enough, ESO and Terraform must agree on the same value.
  dashboards_jwt_secret     = var.enable_dashboards ? random_password.dashboards_jwt[0].result : ""
  dashboards_admin_username = "admin"
  dashboards_admin_password = var.enable_dashboards ? random_password.dashboards_admin[0].result : ""

  # Ops ingress (public Grafana/Alertmanager behind HTTP basic auth): always on, so
  # unlike dashboards_* above it isn't gated by enable_dashboards/enable_bi. Seeded
  # into Vault/Key Vault as "ops-auth" (vault.tf / keyvault.tf) for the chart's
  # ExternalSecret to read.
  ops_user           = "ops"
  ops_admin_password = random_password.ops_admin.result
  # {SHA}<base64 sha1 digest>, matching `htpasswd -s` — see data.external.ops_htpasswd_hash
  # in vault.tf for why this needs to shell out instead of a native function.
  ops_htpasswd = "${local.ops_user}:{SHA}${data.external.ops_htpasswd_hash.result.hash}"
}

check "vault_address_configured" {
  assert {
    condition     = var.vault_address != ""
    error_message = "vault_address must be set. Vault is required for all deployments."
  }
}

data "aws_eks_cluster" "main" {
  count = var.cloud == "aws" ? 1 : 0
  name  = var.eks_cluster_id
}

data "aws_partition" "current" {
  count = var.cloud == "aws" ? 1 : 0
}

data "aws_region" "current" {
  count = var.cloud == "aws" ? 1 : 0
}

data "aws_caller_identity" "current" {
  count = var.cloud == "aws" ? 1 : 0
}

data "aws_ecr_authorization_token" "chart" {
  count = var.cloud == "aws" ? 1 : 0
}

data "aws_kms_key" "s3" {
  count  = var.cloud == "aws" ? 1 : 0
  key_id = var.s3_kms_key_id
}

data "aws_secretsmanager_secret_version" "db_master" {
  count     = var.cloud == "aws" ? 1 : 0
  secret_id = var.primary_db_secret
}

data "aws_secretsmanager_secret_version" "db_bi" {
  count     = var.cloud == "aws" && var.enable_bi ? 1 : 0
  secret_id = var.bi_database_credential_secret
}

data "azurerm_kubernetes_cluster" "main" {
  for_each            = local.azure_instances
  provider            = azurerm.main[each.key]
  name                = var.eks_cluster_id
  resource_group_name = var.azure_resource_group
}

data "azurerm_key_vault_secret" "db_master" {
  for_each     = local.azure_instances
  provider     = azurerm.main[each.key]
  name         = "database-credentials"
  key_vault_id = var.azure_key_vault_id
}