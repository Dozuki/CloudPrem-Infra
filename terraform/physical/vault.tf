# ---------------------------------------------------------------------------
# Vault PrivateLink Endpoint
# Creates a VPC Interface Endpoint and private DNS to reach a centrally
# managed Vault cluster via AWS PrivateLink. Supports cross-region endpoints.
# ---------------------------------------------------------------------------

locals {
  # Extract the service region from the endpoint service name
  # (format: com.amazonaws.vpce.<region>.vpce-svc-xxx)
  vault_service_region = element(split(".", var.vault_endpoint_service_name), 3)
}

resource "aws_security_group" "vault_endpoint" {
  name_prefix = "vault-endpoint-"
  description = "Allow Vault API access from within the VPC"
  vpc_id      = local.vpc_id

  ingress {
    description = "Vault API from VPC"
    from_port   = 8200
    to_port     = 8200
    protocol    = "tcp"
    cidr_blocks = [local.vpc_cidr]
  }

  egress {
    description = "Vault API to endpoint"
    from_port   = 8200
    to_port     = 8200
    protocol    = "tcp"
    cidr_blocks = [local.vpc_cidr]
  }

  tags = {
    Name = "vault-endpoint"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_endpoint" "vault" {
  vpc_id             = local.vpc_id
  service_name       = var.vault_endpoint_service_name
  service_region     = local.vault_service_region
  vpc_endpoint_type  = "Interface"
  subnet_ids         = local.private_subnet_ids
  security_group_ids = [aws_security_group.vault_endpoint.id]

  private_dns_enabled = false

  tags = {
    Name = "vault-endpoint"
  }
}

resource "aws_route53_zone" "vault_private" {
  name = "internal.dozuki.com"

  vpc {
    vpc_id = local.vpc_id
  }

  tags = {
    Name = "vault-private-dns"
  }
}

resource "aws_route53_record" "vault" {
  zone_id = aws_route53_zone.vault_private.zone_id
  name    = "vault.internal.dozuki.com"
  type    = "CNAME"
  ttl     = 300
  records = [aws_vpc_endpoint.vault.dns_entry[0]["dns_name"]]
}
