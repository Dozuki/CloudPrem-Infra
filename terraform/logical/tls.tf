# Gateway TLS. Manual TLS (supplied cert/key on ANY cloud, or a generated self-signed
# cert) keeps cert-manager/ACME out of the way (no public-DNS / ACME dependency —
# essential for ephemeral test clusters and air-gapped on-prem).
#
# SUPPLIED certs are rendered by the chart (tls.enabled + tls.cert/key, typed
# kubernetes.io/tls since chart 0.3.12), NOT by Terraform — so a v6.0 (chart-owned
# tls-secret) -> v6.1 upgrade keeps the same owner and doesn't collide ("secrets
# tls-secret already exists"). Terraform only creates the secret for the GENERATED
# self-signed case (Azure dev), which is greenfield. AWS with no supplied cert is
# unaffected (cert-manager/ACME as before).

locals {
  # Operator-supplied cert/key — cloud-agnostic; rendered by the chart.
  tls_supplied = var.tls_cert != "" && var.tls_key != ""
  # Generated self-signed cert (dev). Azure-only for now; follow-up to generalize.
  tls_selfsigned = var.cloud == "azure" && var.azure_tls_mode == "self-signed"
  # Customer cert stored in Vault, synced into tls-secret by ESO (cert never in TF
  # state). Like self-signed, it's "manual" TLS and the secret is owned externally.
  tls_from_vault = var.customer_tls_externally_managed
  # Any manual TLS -> cert-manager/ACME (dns_validation) stays out of the way.
  tls_manual = local.tls_supplied || local.tls_selfsigned || local.tls_from_vault
  # Terraform creates the tls-secret ONLY for the generated self-signed cert; supplied
  # certs go through the chart (consistent owner across the v6.0->v6.1 upgrade).
  tls_managed_tf = local.tls_selfsigned
  # The chart must skip rendering tls-secret whenever something ELSE owns it — the
  # TF self-signed secret OR the ESO-synced Vault secret.
  tls_externally_managed = local.tls_managed_tf || local.tls_from_vault
}

resource "tls_private_key" "gateway" {
  count     = local.tls_selfsigned ? 1 : 0
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_self_signed_cert" "gateway" {
  count           = local.tls_selfsigned ? 1 : 0
  private_key_pem = tls_private_key.gateway[0].private_key_pem

  subject {
    common_name  = var.dns_domain_name
    organization = "Dozuki MPC (dev self-signed)"
  }

  dns_names             = [var.dns_domain_name]
  validity_period_hours = 8760 # 1 year
  early_renewal_hours   = 720

  allowed_uses = [
    "key_encipherment",
    "digital_signature",
    "server_auth",
  ]
}

resource "kubernetes_secret_v1" "gateway_tls" {
  count = local.tls_managed_tf ? 1 : 0

  metadata {
    name      = "tls-secret"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }

  type = "kubernetes.io/tls"

  # Self-signed only (supplied certs are rendered by the chart). count == tls_selfsigned.
  data = {
    "tls.crt" = tls_self_signed_cert.gateway[0].cert_pem
    "tls.key" = tls_private_key.gateway[0].private_key_pem
  }
}

# Customer-provided TLS: sync the cert+key from Vault (secret/<tenant>/<env>/tls,
# keys cert/key) into the tls-secret K8s Secret via ESO. The cert/key are seeded in
# Vault out-of-band, so they never enter Terraform state. The chart skips rendering
# tls-secret (tls.externallyManaged) and drops the cert-manager Gateway annotation,
# leaving ESO as the sole owner. References the chart-created SecretStore
# vault-<stack_label>; depends on the app release so that SecretStore exists first.
resource "kubernetes_manifest" "tls_external_secret" {
  count = local.tls_from_vault ? 1 : 0

  depends_on = [helm_release.external_secrets, helm_release.app]

  manifest = {
    apiVersion = "external-secrets.io/v1"
    kind       = "ExternalSecret"
    metadata = {
      name      = "tls-secret"
      namespace = kubernetes_namespace_v1.app.metadata[0].name
    }
    spec = {
      refreshInterval = "5m"
      secretStoreRef = {
        name = "vault-${local.vault_stack_label}"
        kind = "SecretStore"
      }
      target = {
        name           = "tls-secret"
        creationPolicy = "Owner"
        deletionPolicy = "Retain"
        template = {
          type = "kubernetes.io/tls"
          data = {
            "tls.crt" = "{{ .cert }}"
            "tls.key" = "{{ .key }}"
          }
        }
      }
      data = [
        {
          secretKey = "cert"
          remoteRef = { key = "${local.vault_env_prefix}/tls", property = "cert" }
        },
        {
          secretKey = "key"
          remoteRef = { key = "${local.vault_env_prefix}/tls", property = "key" }
        },
      ]
    }
  }
}
