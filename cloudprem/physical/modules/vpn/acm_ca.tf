resource "tls_private_key" "ca" {
  algorithm = "RSA"
}
resource "tls_self_signed_cert" "ca" {
  key_algorithm   = "RSA"
  private_key_pem = tls_private_key.ca.private_key_pem

  subject {
    common_name  = "${local.identifier}.${data.aws_region.current.name}.vpn.ca"
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
resource "aws_ssm_parameter" "vpn_ca_key" {
  name        = "${local.ssm_prefix}/acm/vpn/ca_key"
  description = "VPN CA key"
  type        = "SecureString"
  value       = tls_private_key.ca.private_key_pem
}
resource "aws_ssm_parameter" "vpn_ca_cert" {
  name        = "${local.ssm_prefix}/acm/vpn/ca_cert"
  description = "VPN CA cert"
  type        = "SecureString"
  value       = tls_self_signed_cert.ca.cert_pem
}