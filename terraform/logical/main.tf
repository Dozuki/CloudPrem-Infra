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
  # in-Terraform via the ECR token. Azure pulls the
  # chart from GHCR using an ambient `helm registry login` performed by the
  # azure-config bootstrap, so no provider-level registry creds are set there.
  registries = var.cloud == "aws" ? [{
    url      = "oci://${var.image_repository}"
    username = data.aws_ecr_authorization_token.chart[0].user_name
    password = data.aws_ecr_authorization_token.chart[0].password
  }] : []
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

  // Map for app config

  base_config_values = {
    customer               = { value = coalesce(var.customer, "Dozuki") }
    environment            = { value = var.environment }
    aws_acct_id            = { value = var.cloud == "aws" ? data.aws_caller_identity.current[0].account_id : "" }
    aws_region             = { value = var.cloud == "aws" ? data.aws_region.current[0].region : "us-east-1" }
    hostname               = { value = var.dns_domain_name }
    ingress_hostname       = { value = coalesce(var.ingress_hostname, var.dns_domain_name) }
    bi_enabled             = { value = var.enable_bi ? "true" : "false" }
    webhooks_enabled       = { value = var.enable_webhooks ? "true" : "false" }
    memcached_host         = { value = local.memcached_host }
    s3_kms_key             = { value = var.cloud == "aws" ? data.aws_kms_key.s3[0].arn : "" }
    s3_images_bucket       = { value = var.s3_images_bucket }
    s3_objects_bucket      = { value = var.s3_objects_bucket }
    s3_documents_bucket    = { value = var.s3_documents_bucket }
    s3_pdfs_bucket         = { value = var.s3_pdfs_bucket }
    db_host                = { value = local.db_master_host }
    db_user                = { value = local.db_master_username }
    db_password            = { value = local.db_master_password }
    rds_ca_cert            = { value = base64encode(file(local.ca_cert_pem_file)) }
    msk_bootstrap_brokers  = { value = var.msk_bootstrap_brokers }
    google_translate_token = { value = var.google_translate_api_token }
    dns_validation         = { value = !local.is_us_gov && contains(["dozuki.cloud", "dozuki.com", "dozuki.app", "dozuki.guide"], replace(var.dns_domain_name, "/^[^.]+\\./", "")) ? "true" : "false" }
    vault_enabled          = { value = "true" }
    vault_address          = { value = var.vault_address }
    image_repository       = { value = var.image_repository }
    image_tag              = { value = var.image_tag }
    nextjs_tag             = { value = var.nextjs_tag }
    smtp_enabled           = { value = var.smtp_enabled ? "true" : "false" }
    smtp_host              = { value = var.smtp_host }
    smtp_from_address      = { value = var.smtp_from_address }
    smtp_auth_enabled      = { value = var.smtp_auth_enabled ? "true" : "false" }
    smtp_username          = { value = var.smtp_username }
    smtp_password          = { value = var.smtp_password }
  }

  // Optional add-on for Grafana config
  grafana_config_values = {
    grafana_admin_username      = { value = local.grafana_admin_username }
    grafana_admin_password      = { value = local.grafana_admin_password }
    grafana_datasource_hostname = { value = local.db_bi_host }
    grafana_datasource_password = { value = local.db_bi_password }
    grafana_settings_hostname   = { value = local.db_master_host }
    grafana_settings_username   = { value = local.db_master_username }
    grafana_settings_password   = { value = local.db_master_password }
    grafana_subpath             = { value = var.grafana_subpath }
  }

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