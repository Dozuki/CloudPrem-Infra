# Vault integration — stack onboarding (policy, auth) and secret seeding.
#
# When vault is enabled, this file:
#   1. Creates a scoped policy for this environment
#   2. Creates an IAM auth role (for Terraform cross-account access)
#   3. Creates a Kubernetes auth backend + role (for ESO in-cluster access)
#   4. Seeds per-environment secrets with real infrastructure values
#   5. Reads system-wide (global) secrets pre-seeded in vault-infrastructure
#
# When vault is disabled, secrets flow through the devops/app/config
# Secrets Manager secret instead.

locals {
  vault_tenant      = coalesce(var.customer, "dozuki")
  vault_env_prefix  = "${local.vault_tenant}/${var.environment}"
  vault_stack_label = "${local.vault_tenant}-${var.environment}"
}

# --- Stack onboarding: policy, IAM auth role, K8s auth --- #

# Read-write policy for Terraform (IAM auth) to seed per-environment secrets.
resource "vault_policy" "stack" {
  count = var.enable_vault ? 1 : 0
  name  = local.vault_stack_label

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
  count = var.enable_vault ? 1 : 0
  name  = "${local.vault_stack_label}-eso"

  policy = <<-EOT
    path "secret/data/${local.vault_env_prefix}/*" {
      capabilities = ["read", "list"]
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

resource "vault_aws_auth_backend_role" "stack" {
  count = var.enable_vault ? 1 : 0

  backend   = "aws"
  role      = local.vault_stack_label
  auth_type = "iam"

  bound_iam_principal_arns = [
    "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${local.vault_stack_label}-*",
  ]

  token_policies = [vault_policy.stack[0].name]
  token_ttl      = 3600
  token_max_ttl  = 86400
}

resource "vault_auth_backend" "kubernetes" {
  count = var.enable_vault ? 1 : 0

  type        = "kubernetes"
  path        = "k8s/${local.vault_stack_label}"
  description = "Kubernetes auth for ${local.vault_env_prefix}"
}

# Service account for Vault to call the TokenReview API on this cluster.
resource "kubernetes_service_account_v1" "vault_auth" {
  count = var.enable_vault ? 1 : 0

  metadata {
    name      = "vault-auth"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
}

resource "kubernetes_cluster_role_binding_v1" "vault_auth_delegator" {
  count = var.enable_vault ? 1 : 0

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
  count = var.enable_vault ? 1 : 0

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
  count = var.enable_vault ? 1 : 0

  backend              = vault_auth_backend.kubernetes[0].path
  kubernetes_host      = data.aws_eks_cluster.main.endpoint
  kubernetes_ca_cert   = base64decode(data.aws_eks_cluster.main.certificate_authority[0].data)
  token_reviewer_jwt   = kubernetes_secret_v1.vault_auth_token[0].data["token"]
  disable_local_ca_jwt = true
}

resource "vault_kubernetes_auth_backend_role" "eso" {
  count = var.enable_vault ? 1 : 0

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
  count               = var.enable_vault ? 1 : 0
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
  count               = var.enable_vault && var.enable_bi ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/bi"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    host     = local.db_bi_host
    password = local.db_bi_password
  })
}

resource "vault_kv_secret_v2" "cache" {
  count               = var.enable_vault ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/cache"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    host = var.memcached_cluster_address
  })
}

resource "vault_kv_secret_v2" "google_translate" {
  count               = var.enable_vault ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/google-translate"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    token = var.google_translate_api_token
  })
}

resource "vault_kv_secret_v2" "smtp" {
  count               = var.enable_vault ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/smtp"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    password = var.smtp_password
  })
}

# --- System-wide secrets (read from Vault, pre-seeded by vault-infrastructure) --- #

data "vault_kv_secret_v2" "global_sentry" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/sentry"
}

data "vault_kv_secret_v2" "global_frontegg" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/frontegg"
}

data "vault_kv_secret_v2" "global_surveyjs" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/surveyjs"
}

data "vault_kv_secret_v2" "global_rustici" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/rustici"
}

data "vault_kv_secret_v2" "global_ops" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/ops"
}

data "vault_kv_secret_v2" "global_slack" {
  count = var.enable_vault ? 1 : 0
  mount = "secret"
  name  = "dozuki/global/slack"
}

# --- Vault-sourced config values (replace devops/app/config SM when vault enabled) --- #

locals {
  vault_config_values = var.enable_vault ? {
    sentry_dsn                = { value = data.vault_kv_secret_v2.global_sentry[0].data["dsn"] }
    frontegg_client_id        = { value = data.vault_kv_secret_v2.global_frontegg[0].data["clientId"] }
    frontegg_api_token        = { value = data.vault_kv_secret_v2.global_frontegg[0].data["apiToken"] }
    frontegg_docker_username  = { value = data.vault_kv_secret_v2.global_frontegg[0].data["dockerUsername"] }
    frontegg_docker_password  = { value = data.vault_kv_secret_v2.global_frontegg[0].data["dockerPassword"] }
    frontegg_auth_pubkey      = { value = data.vault_kv_secret_v2.global_frontegg[0].data["authPubkey"] }
    surveyjs_license_key      = { value = data.vault_kv_secret_v2.global_surveyjs[0].data["licenseKey"] }
    rustici_password          = { value = data.vault_kv_secret_v2.global_rustici[0].data["password"] }
    rustici_managed_password  = { value = data.vault_kv_secret_v2.global_rustici[0].data["managedPassword"] }
    ops_basic_auth            = { value = data.vault_kv_secret_v2.global_ops[0].data["basicAuth"] }
    infra_auth_password       = { value = data.vault_kv_secret_v2.global_ops[0].data["infraAuthPassword"] }
    slack_webhook_url         = { value = data.vault_kv_secret_v2.global_slack[0].data["webhookUrl"] }
    grafana_smtp_enabled      = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpEnabled"], "false") }
    grafana_smtp_host         = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpHost"], "") }
    grafana_smtp_user         = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpUser"], "") }
    grafana_smtp_password     = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpPassword"], "") }
    grafana_smtp_from_address = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpFromAddress"], "") }
    grafana_smtp_starttls     = { value = try(data.vault_kv_secret_v2.global_ops[0].data["grafanaSmtpStarttls"], "OpportunisticStartTLS") }
  } : {}
}
