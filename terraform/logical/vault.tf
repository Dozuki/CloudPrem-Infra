# ---------------------------------------------------------------------------
# External Secrets Operator + Vault Integration
# Deploys ESO and configures it to pull secrets from a centrally managed
# Vault cluster, replacing Terraform-managed Kubernetes secrets.
# ---------------------------------------------------------------------------

locals {
  vault_customer_name = coalesce(var.customer, "dozuki")
}

resource "kubernetes_namespace" "external_secrets" {
  count = var.enable_vault ? 1 : 0

  metadata {
    name = "external-secrets"
  }
}

resource "helm_release" "external_secrets" {
  count = var.enable_vault ? 1 : 0

  name       = "external-secrets"
  repository = "https://charts.external-secrets.io"
  chart      = "external-secrets"
  version    = "0.10.7"
  namespace  = kubernetes_namespace.external_secrets[0].metadata[0].name

  wait    = true
  timeout = 300

  set {
    name  = "installCRDs"
    value = "true"
  }
}

resource "kubernetes_manifest" "vault_secret_store" {
  count = var.enable_vault ? 1 : 0

  depends_on = [helm_release.external_secrets]

  manifest = {
    apiVersion = "external-secrets.io/v1beta1"
    kind       = "SecretStore"
    metadata = {
      name      = "vault-backend"
      namespace = local.k8s_namespace_name
    }
    spec = {
      provider = {
        vault = {
          server  = var.vault_address
          path    = "secret"
          version = "v2"
          auth = {
            kubernetes = {
              mountPath = var.vault_auth_mount_path
              role      = "dozuki-app"
              serviceAccountRef = {
                name = "external-secrets"
              }
            }
          }
        }
      }
    }
  }
}

resource "kubernetes_manifest" "vault_external_secret" {
  count = var.enable_vault ? 1 : 0

  depends_on = [kubernetes_manifest.vault_secret_store]

  manifest = {
    apiVersion = "external-secrets.io/v1beta1"
    kind       = "ExternalSecret"
    metadata = {
      name      = "dozuki-infra-credentials"
      namespace = local.k8s_namespace_name
    }
    spec = {
      refreshInterval = "5m"
      secretStoreRef = {
        name = "vault-backend"
        kind = "SecretStore"
      }
      target = {
        name           = "dozuki-infra-credentials"
        creationPolicy = "Owner"
        deletionPolicy = "Retain"
      }
      data = [
        {
          secretKey = "master_host"
          remoteRef = {
            key      = "secret/${local.vault_customer_name}/db/primary"
            property = "host"
          }
        },
        {
          secretKey = "master_user"
          remoteRef = {
            key      = "secret/${local.vault_customer_name}/db/primary"
            property = "username"
          }
        },
        {
          secretKey = "master_password"
          remoteRef = {
            key      = "secret/${local.vault_customer_name}/db/primary"
            property = "password"
          }
        },
        {
          secretKey = "bi_host"
          remoteRef = {
            key      = "secret/${local.vault_customer_name}/db/bi"
            property = "host"
          }
        },
        {
          secretKey = "bi_user"
          remoteRef = {
            key      = "secret/${local.vault_customer_name}/db/bi"
            property = "username"
          }
        },
        {
          secretKey = "bi_password"
          remoteRef = {
            key      = "secret/${local.vault_customer_name}/db/bi"
            property = "password"
          }
        },
        {
          secretKey = "memcached_host"
          remoteRef = {
            key      = "secret/${local.vault_customer_name}/infra/cache"
            property = "memcached_host"
          }
        },
      ]
    }
  }
}
