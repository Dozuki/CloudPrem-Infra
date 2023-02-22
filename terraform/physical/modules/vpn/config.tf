
module "ssl_cert" {
  source      = "../acm"
  environment = var.environment
  identifier  = var.identifier

  namespace = "vpn"
}

resource "tls_private_key" "client" {
  count     = length(var.vpn-client-list)
  algorithm = "RSA"
}
resource "tls_cert_request" "client" {
  count           = length(var.vpn-client-list)
  key_algorithm   = "RSA"
  private_key_pem = tls_private_key.client[count.index].private_key_pem
  subject {
    common_name  = "${local.identifier}.${data.aws_region.current.name}.vpn.${var.vpn-client-list[count.index]}-client"
    organization = local.identifier
  }
}
resource "tls_locally_signed_cert" "client" {
  count                 = length(var.vpn-client-list)
  cert_request_pem      = tls_cert_request.client[count.index].cert_request_pem
  ca_key_algorithm      = "RSA"
  ca_private_key_pem    = module.ssl_cert.ssm_ca_key.value
  ca_cert_pem           = module.ssl_cert.ssm_ca_cert.value
  validity_period_hours = 87600
  allowed_uses = [
    "key_encipherment",
    "digital_signature",
    "client_auth",
  ]
}

resource "aws_acm_certificate" "client" {
  count             = length(var.vpn-client-list)
  private_key       = tls_private_key.client[count.index].private_key_pem
  certificate_body  = tls_locally_signed_cert.client[count.index].cert_pem
  certificate_chain = module.ssl_cert.ssm_ca_cert.value

  tags = merge(
    local.tags,
    {
      Tier         = "Private"
      CostType     = "AlwaysCreated"
      BackupPolicy = "n/a"
    }
  )
}

resource "aws_s3_bucket_object" "vpn-config-file" {
  count                  = length(var.vpn-client-list)
  bucket                 = aws_s3_bucket.vpn-config-files.id
  server_side_encryption = "aws:kms"
  key                    = "${var.vpn-client-list[count.index]}-vpn.ovpn"
  content_base64 = base64encode(<<-EOT
client
dev tun
proto udp
remote ${aws_ec2_client_vpn_endpoint.vpn-client.id}.prod.clientvpn.${data.aws_region.current.name}.amazonaws.com 443
remote-random-hostname
resolv-retry infinite
nobind
remote-cert-tls server
cipher AES-256-GCM
--inactive 300 100
verb 3

<ca>
${module.ssl_cert.ssm_ca_cert.value}
</ca>

reneg-sec 0

<cert>
${aws_ssm_parameter.vpn_client_cert[count.index].value}
</cert>

<key>
${aws_ssm_parameter.vpn_client_key[count.index].value}
</key>
    EOT
  )
}

resource "aws_ssm_parameter" "vpn_client_key" {
  count       = length(var.vpn-client-list)
  name        = "${local.ssm_prefix}/acm/vpn/${var.vpn-client-list[count.index]}_client_key"
  description = "VPN ${var.vpn-client-list[count.index]} client key"
  type        = "SecureString"
  value       = tls_private_key.client[count.index].private_key_pem
}
resource "aws_ssm_parameter" "vpn_client_cert" {
  count       = length(var.vpn-client-list)
  name        = "${local.ssm_prefix}/acm/vpn/${var.vpn-client-list[count.index]}_client_cert"
  description = "VPN ${var.vpn-client-list[count.index]} client cert"
  type        = "SecureString"
  value       = tls_locally_signed_cert.client[count.index].cert_pem
}