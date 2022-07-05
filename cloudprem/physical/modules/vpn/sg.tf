#tfsec:ignore:aws-vpc-no-public-egress-sgr
resource "aws_security_group" "vpn" {
  name        = "${local.identifier}-vpn-security-group"
  description = "Allows access to connect to the VPN and unlimited egress."

  vpc_id = var.vpc_id
  ingress {
    description = "Allow VPN connection from specified CIDRs"
    from_port   = 443
    to_port     = 443
    protocol    = "udp"
    cidr_blocks = var.allowed_ingress_cidrs
  }
  egress {
    description = "Allow egress to internet"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = merge(
    local.tags,
    {
      Name = "${local.identifier}-vpn-security-group"
    }
  )
}