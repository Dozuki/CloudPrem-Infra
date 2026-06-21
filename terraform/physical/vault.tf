# ---------------------------------------------------------------------------
# Vault PrivateLink Endpoint
# Creates a VPC Interface Endpoint and private DNS to reach a centrally
# managed Vault cluster via AWS PrivateLink. Supports cross-region endpoints.
# Skipped entirely when var.vault_endpoint_service_name is "" — for environments
# with no central Vault (e.g. the research/Cilium sandbox).
# ---------------------------------------------------------------------------

locals {
  # Only provision the Vault endpoint when a service name is configured.
  create_vault_endpoint = var.vault_endpoint_service_name != ""
  # Extract the service region from the endpoint service name
  # (format: com.amazonaws.vpce.<region>.vpce-svc-xxx)
  vault_service_region  = local.create_vault_endpoint ? element(split(".", var.vault_endpoint_service_name), 3) : ""
  vault_is_cross_region = local.create_vault_endpoint && local.vault_service_region != data.aws_region.current.id
}

# Look up endpoint service AZs for same-region deployments to filter subnets.
# Cross-region lookups don't work (data source doesn't support service_region),
# so we skip the filter and pass all subnets — AWS maps all consumer AZs for
# cross-region endpoints.
data "aws_vpc_endpoint_service" "vault" {
  count        = local.create_vault_endpoint && !local.vault_is_cross_region ? 1 : 0
  service_name = var.vault_endpoint_service_name
}

data "aws_subnets" "vault_compatible" {
  count = local.create_vault_endpoint && !local.vault_is_cross_region ? 1 : 0

  filter {
    name   = "subnet-id"
    values = local.private_subnet_ids
  }

  filter {
    name   = "availability-zone"
    values = data.aws_vpc_endpoint_service.vault[0].availability_zones
  }
}

resource "aws_security_group" "vault_endpoint" {
  count       = local.create_vault_endpoint ? 1 : 0
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
  count              = local.create_vault_endpoint ? 1 : 0
  vpc_id             = local.vpc_id
  service_name       = var.vault_endpoint_service_name
  service_region     = local.vault_service_region
  vpc_endpoint_type  = "Interface"
  subnet_ids         = local.vault_is_cross_region ? local.private_subnet_ids : data.aws_subnets.vault_compatible[0].ids
  security_group_ids = [aws_security_group.vault_endpoint[0].id]

  private_dns_enabled = false

  tags = {
    Name = "vault-endpoint"
  }
}

resource "aws_route53_zone" "vault_private" {
  count = local.create_vault_endpoint ? 1 : 0
  name  = "internal.dozuki.com"

  vpc {
    vpc_id = local.vpc_id
  }

  tags = {
    Name = "vault-private-dns"
  }
}

resource "aws_route53_record" "vault" {
  count   = local.create_vault_endpoint ? 1 : 0
  zone_id = aws_route53_zone.vault_private[0].zone_id
  name    = "vault.internal.dozuki.com"
  type    = "CNAME"
  ttl     = 300
  records = [aws_vpc_endpoint.vault[0].dns_entry[0]["dns_name"]]
}

# Preserve resource addresses for already-deployed environments (which set a
# non-empty vault_endpoint_service_name) so adding count does not force replacement.
moved {
  from = aws_security_group.vault_endpoint
  to   = aws_security_group.vault_endpoint[0]
}
moved {
  from = aws_vpc_endpoint.vault
  to   = aws_vpc_endpoint.vault[0]
}
moved {
  from = aws_route53_zone.vault_private
  to   = aws_route53_zone.vault_private[0]
}
moved {
  from = aws_route53_record.vault
  to   = aws_route53_record.vault[0]
}
