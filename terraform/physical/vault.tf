# ---------------------------------------------------------------------------
# Vault PrivateLink Endpoint
# Creates a VPC Interface Endpoint and private DNS to reach a centrally
# managed Vault cluster via AWS PrivateLink.
# ---------------------------------------------------------------------------

data "aws_vpc_endpoint_service" "vault" {
  service_name = var.vault_endpoint_service_name

  lifecycle {
    precondition {
      condition     = var.vault_endpoint_service_name != ""
      error_message = "vault_endpoint_service_name is required. Deploy vault-privatelink-service first."
    }
  }
}

# Filter private subnets to only those in AZs supported by the endpoint
# service. AZ name-to-ID mappings differ per account, so the service may
# not cover every AZ the customer VPC uses.
data "aws_subnets" "vault_compatible" {
  filter {
    name   = "subnet-id"
    values = local.private_subnet_ids
  }

  filter {
    name   = "availability-zone"
    values = data.aws_vpc_endpoint_service.vault.availability_zones
  }

  lifecycle {
    postcondition {
      condition     = length(self.ids) > 0
      error_message = "No private subnets overlap with the Vault endpoint service AZs. Check cross-account AZ mappings."
    }
  }
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
  vpc_endpoint_type  = "Interface"
  subnet_ids         = data.aws_subnets.vault_compatible.ids
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
