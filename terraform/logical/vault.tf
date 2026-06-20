# Vault integration — stack onboarding (policy, auth) and secret seeding.
#
# This file:
#   1. Creates a scoped policy for this environment
#   2. Creates an IAM auth role (for Terraform cross-account access)
#   3. Creates a Kubernetes auth backend + role (for ESO in-cluster access)
#   4. Seeds per-environment secrets with real infrastructure values
#   5. Reads system-wide (global) secrets pre-seeded in vault-infrastructure

locals {
  vault_tenant      = coalesce(var.customer, "dozuki")
  vault_env_prefix  = "${local.vault_tenant}/${var.environment}"
  vault_stack_label = "${local.vault_tenant}-${var.environment}"
}

# --- Stack onboarding: policy, IAM auth role, K8s auth --- #

# Read-write policy for Terraform (IAM auth) to seed per-environment secrets.
resource "vault_policy" "stack" {
  count = var.cloud == "aws" ? 1 : 0

  name = local.vault_stack_label

  policy = <<-EOT
    path "secret/data/${local.vault_env_prefix}/*" {
      capabilities = ["create", "read", "update", "delete", "list"]
    }
    path "secret/metadata/${local.vault_env_prefix}/*" {
      capabilities = ["read", "list"]
    }
    path "secret/data/dozuki/global/*" {
      capabilities = ["read", "list"]
    }
    path "secret/metadata/dozuki/global/*" {
      capabilities = ["read", "list"]
    }
  EOT
}

# Read-only policy for ESO (K8s auth) — only needs to read secrets.
resource "vault_policy" "eso_readonly" {
  count = var.cloud == "aws" ? 1 : 0

  name = "${local.vault_stack_label}-eso"

  policy = <<-EOT
    path "secret/data/${local.vault_env_prefix}/*" {
      capabilities = ["read"]
    }
    path "secret/metadata/${local.vault_env_prefix}/*" {
      capabilities = ["read", "list"]
    }
    path "secret/data/dozuki/global/*" {
      capabilities = ["read"]
    }
    path "secret/metadata/dozuki/global/*" {
      capabilities = ["read", "list"]
    }
  EOT
}

resource "vault_aws_auth_backend_role" "stack" {
  count = var.cloud == "aws" ? 1 : 0

  backend   = "aws"
  role      = local.vault_stack_label
  auth_type = "iam"

  bound_iam_principal_arns = [
    "arn:${data.aws_partition.current[0].partition}:iam::${data.aws_caller_identity.current[0].account_id}:role/${local.vault_stack_label}-*",
  ]

  token_policies = [vault_policy.stack[0].name]
  token_ttl      = 3600
  token_max_ttl  = 86400
}

resource "vault_auth_backend" "kubernetes" {
  count = var.cloud == "aws" ? 1 : 0

  type        = "kubernetes"
  path        = "k8s/${local.vault_stack_label}"
  description = "Kubernetes auth for ${local.vault_env_prefix}"
}

# Service account for Vault to call the TokenReview API on this cluster.
resource "kubernetes_service_account_v1" "vault_auth" {
  count = var.cloud == "aws" ? 1 : 0

  metadata {
    name      = "vault-auth"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
}

resource "kubernetes_cluster_role_binding_v1" "vault_auth_delegator" {
  count = var.cloud == "aws" ? 1 : 0

  metadata {
    name = "vault-auth-delegator"
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "system:auth-delegator"
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account_v1.vault_auth[0].metadata[0].name
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
}

# Long-lived token secret for the vault-auth service account.
resource "kubernetes_secret_v1" "vault_auth_token" {
  count = var.cloud == "aws" ? 1 : 0

  metadata {
    name      = "vault-auth-token"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
    annotations = {
      "kubernetes.io/service-account.name" = kubernetes_service_account_v1.vault_auth[0].metadata[0].name
    }
  }

  type = "kubernetes.io/service-account-token"
}

resource "vault_kubernetes_auth_backend_config" "stack" {
  count = var.cloud == "aws" ? 1 : 0

  backend              = vault_auth_backend.kubernetes[0].path
  kubernetes_host      = data.aws_eks_cluster.main[0].endpoint
  kubernetes_ca_cert   = base64decode(data.aws_eks_cluster.main[0].certificate_authority[0].data)
  token_reviewer_jwt   = kubernetes_secret_v1.vault_auth_token[0].data["token"]
  disable_local_ca_jwt = true
}

resource "vault_kubernetes_auth_backend_role" "eso" {
  count = var.cloud == "aws" ? 1 : 0

  backend   = vault_auth_backend.kubernetes[0].path
  role_name = "dozuki-app"

  bound_service_account_names      = ["dozuki-external-secrets"]
  bound_service_account_namespaces = [local.k8s_namespace_name]
  audience                         = var.vault_address

  token_policies = [vault_policy.eso_readonly[0].name]
  token_ttl      = 3600
  token_max_ttl  = 86400
}

# --- Per-environment secrets (seeded by Terraform) --- #

resource "vault_kv_secret_v2" "db" {
  count               = var.cloud == "aws" ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/db"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    host     = local.db_master_host
    username = local.db_master_username
    password = local.db_master_password
  })
}

resource "vault_kv_secret_v2" "bi" {
  count               = var.cloud == "aws" && var.enable_bi ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/bi"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    host     = local.db_bi_host
    password = local.db_bi_password
  })
}

resource "vault_kv_secret_v2" "cache" {
  count               = var.cloud == "aws" ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/cache"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    # ESO syncs this into the app's memcached.json (overriding the chart config map), so it
    # must be the in-cluster FQDN, not the (empty when in-cluster) ElastiCache address.
    host = local.memcached_host
  })
}

resource "vault_kv_secret_v2" "google_translate" {
  count               = var.cloud == "aws" ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/google-translate"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    token = var.google_translate_api_token
  })
}

resource "vault_kv_secret_v2" "smtp" {
  count               = var.cloud == "aws" ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/smtp"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    password = var.smtp_password
  })
}

# --- System-wide secrets (read from Vault, pre-seeded by vault-infrastructure) --- #

data "vault_kv_secret_v2" "global_sentry" {
  count = var.cloud == "aws" ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/sentry"
}

data "vault_kv_secret_v2" "global_frontegg" {
  count = var.cloud == "aws" ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/frontegg"
}

data "vault_kv_secret_v2" "global_surveyjs" {
  count = var.cloud == "aws" ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/surveyjs"
}

data "vault_kv_secret_v2" "global_rustici" {
  count = var.cloud == "aws" ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/rustici"
}

data "vault_kv_secret_v2" "global_ops" {
  count = var.cloud == "aws" ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/ops"
}

data "vault_kv_secret_v2" "global_slack" {
  count = var.cloud == "aws" ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/slack"
}

# --- Vault-sourced config values (replace devops/app/config SM when vault enabled) --- #

locals {
  vault_config_values = {
    sentry_dsn                = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_sentry[0].data["dsn"] : var.sentry_dsn }
    frontegg_client_id        = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_frontegg[0].data["clientId"] : var.frontegg_client_id }
    frontegg_api_token        = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_frontegg[0].data["apiToken"] : var.frontegg_api_token }
    frontegg_docker_username  = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_frontegg[0].data["dockerUsername"] : "" }
    frontegg_docker_password  = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_frontegg[0].data["dockerPassword"] : "" }
    frontegg_auth_pubkey      = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_frontegg[0].data["authPubkey"] : "" }
    surveyjs_license_key      = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_surveyjs[0].data["licenseKey"] : var.surveyjs_license_key }
    rustici_password          = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_rustici[0].data["password"] : var.rustici_password }
    rustici_managed_password  = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_rustici[0].data["managedPassword"] : var.rustici_managed_password }
    ops_basic_auth            = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_ops[0].data["basicAuth"] : "" }
    infra_auth_password       = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_ops[0].data["infraAuthPassword"] : "" }
    slack_webhook_url         = { value = var.cloud == "aws" ? data.vault_kv_secret_v2.global_slack[0].data["webhookUrl"] : "" }
    grafana_smtp_enabled      = { value = var.cloud == "aws" ? try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpEnabled"], "false") : "false" }
    grafana_smtp_host         = { value = var.cloud == "aws" ? try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpHost"], "") : "" }
    grafana_smtp_user         = { value = var.cloud == "aws" ? try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpUser"], "") : "" }
    grafana_smtp_password     = { value = var.cloud == "aws" ? try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpPassword"], "") : "" }
    grafana_smtp_from_address = { value = var.cloud == "aws" ? try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpFromAddress"], "") : "" }
    grafana_smtp_starttls     = { value = var.cloud == "aws" ? try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpStarttls"], "OpportunisticStartTLS") : "OpportunisticStartTLS" }
  }
}

# --- State moves: resources gained `count` when the azure cloud gate was added --- #

moved {
  from = vault_policy.stack
  to   = vault_policy.stack[0]
}

moved {
  from = vault_policy.eso_readonly
  to   = vault_policy.eso_readonly[0]
}

moved {
  from = vault_aws_auth_backend_role.stack
  to   = vault_aws_auth_backend_role.stack[0]
}

moved {
  from = vault_auth_backend.kubernetes
  to   = vault_auth_backend.kubernetes[0]
}

moved {
  from = vault_kubernetes_auth_backend_config.stack
  to   = vault_kubernetes_auth_backend_config.stack[0]
}

moved {
  from = vault_kubernetes_auth_backend_role.eso
  to   = vault_kubernetes_auth_backend_role.eso[0]
}

moved {
  from = vault_kv_secret_v2.db
  to   = vault_kv_secret_v2.db[0]
}

moved {
  from = vault_kv_secret_v2.cache
  to   = vault_kv_secret_v2.cache[0]
}

moved {
  from = vault_kv_secret_v2.google_translate
  to   = vault_kv_secret_v2.google_translate[0]
}

moved {
  from = vault_kv_secret_v2.smtp
  to   = vault_kv_secret_v2.smtp[0]
}

moved {
  from = kubernetes_service_account_v1.vault_auth
  to   = kubernetes_service_account_v1.vault_auth[0]
}

moved {
  from = kubernetes_cluster_role_binding_v1.vault_auth_delegator
  to   = kubernetes_cluster_role_binding_v1.vault_auth_delegator[0]
}

moved {
  from = kubernetes_secret_v1.vault_auth_token
  to   = kubernetes_secret_v1.vault_auth_token[0]
}
