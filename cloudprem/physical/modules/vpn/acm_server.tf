# AWS ACM certificate
resource "aws_acm_certificate" "server" {
  private_key       = tls_private_key.server.private_key_pem
  certificate_body  = tls_locally_signed_cert.server.cert_pem
  certificate_chain = tls_self_signed_cert.ca.cert_pem

  tags = local.tags
}

# TLS certificate and key
resource "tls_private_key" "server" {
  algorithm = "RSA"
}
resource "tls_cert_request" "server" {
  key_algorithm   = "RSA"
  private_key_pem = tls_private_key.server.private_key_pem
  subject {
    common_name  = "${local.identifier}.${data.aws_region.current.name}.vpn.server"
    organization = local.identifier
  }
}
resource "tls_locally_signed_cert" "server" {
  cert_request_pem      = tls_cert_request.server.cert_request_pem
  ca_key_algorithm      = "RSA"
  ca_private_key_pem    = tls_private_key.ca.private_key_pem
  ca_cert_pem           = tls_self_signed_cert.ca.cert_pem
  validity_period_hours = 87600
  allowed_uses = [
    "key_encipherment",
    "digital_signature",
    "server_auth",
  ]
}

# AWS SSM records
resource "aws_ssm_parameter" "vpn_server_key" {
  name        = "${local.ssm_prefix}/acm/vpn/server_key"
  description = "VPN server key"
  type        = "SecureString"
  value       = tls_private_key.server.private_key_pem

  #  tags = local.tags
}
resource "aws_ssm_parameter" "vpn_server_cert" {
  name        = "${local.ssm_prefix}/acm/vpn/server_cert"
  description = "VPN server cert"
  type        = "SecureString"
  value       = tls_locally_signed_cert.server.cert_pem

  #  tags = local.tags
}