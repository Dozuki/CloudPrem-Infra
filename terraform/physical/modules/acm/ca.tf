
resource "tls_private_key" "ca" {
  algorithm = "RSA"
}
resource "tls_self_signed_cert" "ca" {
  private_key_pem = tls_private_key.ca.private_key_pem

  dns_names = [local.ssl_ca_cn]

  subject {
    organization = local.identifier
  }
  validity_period_hours = 87600
  is_ca_certificate     = true
  allowed_uses = [
    "cert_signing",
    "crl_signing",
  ]
}

resource "aws_acm_certificate" "ca" {
  private_key      = tls_private_key.ca.private_key_pem
  certificate_body = tls_self_signed_cert.ca.cert_pem

  tags = local.tags
}

# AWS SSM records
resource "aws_ssm_parameter" "ca_key" {
  name        = "${local.ssm_prefix}/acm/${var.namespace}/ca_key"
  description = "General CA key"
  type        = "SecureString"
  value       = tls_private_key.ca.private_key_pem
}
resource "aws_ssm_parameter" "ca_cert" {
  name        = "${local.ssm_prefix}/acm/${var.namespace}/ca_cert"
  description = "General CA cert"
  type        = "SecureString"
  value       = tls_self_signed_cert.ca.cert_pem
}