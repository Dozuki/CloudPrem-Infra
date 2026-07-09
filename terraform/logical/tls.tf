# Gateway TLS. Manual TLS (supplied cert/key on ANY cloud, or a generated self-signed
# cert) keeps cert-manager/ACME out of the way (no public-DNS / ACME dependency —
# essential for ephemeral test clusters and air-gapped on-prem).
#
# SUPPLIED certs on AWS flow tls_cert/tls_key -> Vault (TF-seeded below) -> ESO ->
# tls-secret, so all customer TLS material starts in terraform inputs and no Vault
# path is ever hand-seeded. On azure/onprem (no Vault) supplied certs are rendered
# by the chart (tls.enabled + tls.cert/key, typed kubernetes.io/tls since chart
# 0.3.12), NOT by Terraform — so a v6.0 (chart-owned tls-secret) -> v6.1 upgrade
# keeps the same owner and doesn't collide ("secrets tls-secret already exists").
# Terraform only creates the K8s secret directly for the GENERATED self-signed case
# (Azure dev), which is greenfield. AWS with no supplied cert is unaffected
# (cert-manager/ACME as before).

locals {
  # Operator-supplied cert/key via tls_cert/tls_key (env.hcl or stack TF_VARs).
  tls_supplied = var.tls_cert != "" && var.tls_key != ""
  # Generated self-signed cert (dev). Azure-only for now; follow-up to generalize.
  tls_selfsigned = var.cloud == "azure" && var.azure_tls_mode == "self-signed"
  # On AWS (Vault always enabled), supplied certs are seeded into Vault by TF and
  # delivered by ESO, so customer info flows env.hcl -> Terraform -> Vault -> pod
  # with no hand-seeding. Azure/onprem supplied certs stay chart-rendered.
  tls_vault_seeded = local.tls_supplied && var.cloud == "aws"
  # tls-secret owned by ESO, from Vault: TF-seeded (above), or the legacy
  # hand-seeded mode (customer_tls_externally_managed; 3m/qa) kept until its
  # cert is migrated into stack vars.
  tls_from_vault = var.customer_tls_externally_managed || local.tls_vault_seeded
  # Any manual TLS -> cert-manager/ACME (dns_validation) stays out of the way.
  tls_manual = local.tls_supplied || local.tls_selfsigned || local.tls_from_vault
  # Terraform creates the tls-secret ONLY for the generated self-signed cert.
  tls_managed_tf = local.tls_selfsigned
  # The chart must skip rendering tls-secret whenever something ELSE owns it — the
  # TF self-signed secret OR the ESO-synced Vault secret.
  tls_externally_managed = local.tls_managed_tf || local.tls_from_vault
  # Supplied certs the CHART renders (azure/onprem, no Vault in the path).
  tls_chart_rendered = local.tls_supplied && !local.tls_vault_seeded
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

# Supplied cert on AWS: TF seeds secret/<tenant>/<env>/tls like every other
# per-env secret (vault.tf), and ESO below delivers it. Raw PEM in Vault (the
# tls_cert/tls_key vars are base64) to match the ESO template and the legacy
# hand-seeded layout, so a migrating env just gets a new KV version.
resource "vault_kv_secret_v2" "tls" {
  count               = local.tls_vault_seeded ? 1 : 0
  mount               = "secret"
  name                = "${local.vault_env_prefix}/tls"
  delete_all_versions = !var.protect_resources

  data_json = jsonencode({
    cert = base64decode(var.tls_cert)
    key  = base64decode(var.tls_key)
  })
}

# Customer-provided TLS: sync the cert+key from Vault (secret/<tenant>/<env>/tls,
# keys cert/key) into the tls-secret K8s Secret via ESO. The Vault entry is either
# TF-seeded (above) or, legacy, hand-seeded (customer_tls_externally_managed). The
# chart skips rendering tls-secret (tls.externallyManaged) and drops the
# cert-manager Gateway annotation, leaving ESO as the sole owner. References the
# chart-created SecretStore vault-<stack_label>; depends on the app release so
# that SecretStore exists first.
resource "kubernetes_manifest" "tls_external_secret" {
  count = local.tls_from_vault ? 1 : 0

  depends_on = [helm_release.external_secrets, helm_release.app, vault_kv_secret_v2.tls]

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
