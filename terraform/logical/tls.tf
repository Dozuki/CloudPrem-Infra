# Gateway TLS managed by Terraform. When an operator supplies a cert/key
# (tls_cert/tls_key) — on ANY cloud — Terraform creates the tls-secret directly and
# the chart runs with tls.externallyManaged=true, so cert-manager/ACME stays out of
# the way (no public-DNS / ACME dependency, which is essential for ephemeral test
# clusters and air-gapped on-prem). Azure additionally supports a generated
# self-signed cert for dev (azure_tls_mode=self-signed); making the self-signed
# generation cloud-agnostic is a follow-up. AWS with no supplied cert is unaffected
# (cert-manager/ACME as before).

locals {
  # Operator-supplied cert/key — cloud-agnostic.
  tls_supplied = var.tls_cert != "" && var.tls_key != ""
  # Generated self-signed cert (dev). Azure-only for now; follow-up to generalize.
  tls_selfsigned = var.cloud == "azure" && var.azure_tls_mode == "self-signed"
  tls_managed_tf = local.tls_supplied || local.tls_selfsigned
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

  data = {
    "tls.crt" = local.tls_supplied ? base64decode(var.tls_cert) : tls_self_signed_cert.gateway[0].cert_pem
    "tls.key" = local.tls_supplied ? base64decode(var.tls_key) : tls_private_key.gateway[0].private_key_pem
  }
}
