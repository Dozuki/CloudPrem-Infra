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

  # ESO syncs this into the app's memcached.json (overriding the chart config map), so it
  # must be the in-cluster FQDN, not the (empty when in-cluster) ElastiCache address.
  data_json = jsonencode({
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

# --- Dashboards (shared Grafana) --- #

resource "random_password" "dashboards_jwt" {
  count = var.enable_dashboards ? 1 : 0

  length  = 40
  special = false
}

resource "random_password" "dashboards_admin" {
  count = var.enable_dashboards ? 1 : 0

  length  = 20
  special = false
}

# Keys match the chart's ESO remoteRef properties exactly (dozuki chart
# templates/vault/external-secret.yaml + templates/azure/external-secret.yaml):
# "secret" signs/verifies the Envoy JWT SecurityPolicy and is baked into
# grafana.json for the app to mint tokens with; adminUser/adminPassword seed the
# dozuki-grafana-admin Secret the bundled Grafana subchart logs in with.
resource "vault_kv_secret_v2" "grafana" {
  count               = var.cloud == "aws" && var.enable_dashboards ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/grafana"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    secret        = local.dashboards_jwt_secret
    adminUser     = local.dashboards_admin_username
    adminPassword = local.dashboards_admin_password
  })
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
