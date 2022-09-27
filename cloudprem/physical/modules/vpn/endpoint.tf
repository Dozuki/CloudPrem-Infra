resource "aws_ec2_client_vpn_endpoint" "vpn-client" {
  description            = "${local.identifier}-vpn-client"
  server_certificate_arn = module.ssl_cert.acm_server_arn
  client_cidr_block      = var.client_cidr_block

  split_tunnel = true
  authentication_options {
    type                       = "certificate-authentication"
    root_certificate_chain_arn = aws_acm_certificate.client[0].arn
  }
  connection_log_options {
    enabled               = true
    cloudwatch_log_group  = aws_cloudwatch_log_group.vpn-logs.name
    cloudwatch_log_stream = aws_cloudwatch_log_stream.vpn-logs-stream.name
  }
  tags = merge(
    local.tags,
    {
      Name = "${local.identifier}-vpn-client"
    }
  )
}
resource "aws_ec2_client_vpn_network_association" "vpn-client" {
  client_vpn_endpoint_id = aws_ec2_client_vpn_endpoint.vpn-client.id
  subnet_id              = var.subnet_id
  security_groups        = [aws_security_group.vpn.id]
}
resource "aws_ec2_client_vpn_authorization_rule" "vpn-client" {
  client_vpn_endpoint_id = aws_ec2_client_vpn_endpoint.vpn-client.id
  target_network_cidr    = "0.0.0.0/0"
  authorize_all_groups   = true
  depends_on = [
    aws_ec2_client_vpn_endpoint.vpn-client,
    aws_ec2_client_vpn_network_association.vpn-client
  ]
}