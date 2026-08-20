# ---------------------------------------------------------------------------
# Mimir PrivateLink Endpoint
# Creates a VPC Interface Endpoint and private DNS so this cluster's Prometheus
# can remote_write to the central Grafana Mimir. Same shape as vault.tf,
# including the cross-region handling, but gated on enable_mimir: env.hcl holds
# one infra_version for both layers, so this file lands on every env the moment
# it bumps to the release carrying it. Default-off is what keeps that safe, the
# code arrives fleet-wide and the resources do not.
# ---------------------------------------------------------------------------

locals {
  # enable_mimir is nullable so its default can depend on the partition. A bare
  # `default = true` would turn this on in GovCloud too and trip the precondition below on
  # both existing gov envs the moment they bump infra_version. Null resolves to on in
  # commercial and off in gov; an explicit true/false always wins, which is how every
  # already-enrolled env keeps rendering byte-identical.
  mimir_enabled = var.enable_mimir != null ? var.enable_mimir : !local.is_us_gov

  # Extract the service region from the endpoint service name
  # (format: com.amazonaws.vpce.<region>.vpce-svc-xxx). The split runs even while the
  # feature is off, so it is guarded - but the guard is belt only: the variable rejects
  # empty and is not nullable, so this branch is unreachable. Do not read the guard as
  # "empty is a supported off switch". Empty would resolve mimir_is_cross_region to false
  # and send "" to the EC2 API through the data source below, which is why the variable
  # refuses it instead.
  mimir_service_region  = var.mimir_endpoint_service_name != "" ? element(split(".", var.mimir_endpoint_service_name), 3) : ""
  mimir_is_cross_region = local.mimir_service_region != "" && local.mimir_service_region != data.aws_region.current.region
}

# Look up endpoint service AZs for same-region deployments to filter subnets.
# Cross-region lookups don't work (data source doesn't support service_region),
# so we skip the filter and pass all subnets. AWS maps all consumer AZs for
# cross-region endpoints. Environments outside the Mimir region always take the cross-region path.
data "aws_vpc_endpoint_service" "mimir" {
  count        = local.mimir_enabled && !local.mimir_is_cross_region ? 1 : 0
  service_name = var.mimir_endpoint_service_name
}

data "aws_subnets" "mimir_compatible" {
  count = local.mimir_enabled && !local.mimir_is_cross_region ? 1 : 0

  filter {
    name   = "subnet-id"
    values = local.private_subnet_ids
  }

  filter {
    name   = "availability-zone"
    values = data.aws_vpc_endpoint_service.mimir[0].availability_zones
  }
}

resource "aws_security_group" "mimir_endpoint" {
  count = local.mimir_enabled ? 1 : 0

  name_prefix = "mimir-endpoint-"
  description = "Allow Mimir ingest access from within the VPC"
  vpc_id      = local.vpc_id

  ingress {
    description = "Mimir ingest from VPC"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = [local.vpc_cidr]
  }

  egress {
    description = "Mimir ingest to endpoint"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = [local.vpc_cidr]
  }

  tags = merge(
    {
      Name = "mimir-endpoint"
    },
    local.tags
  )

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_endpoint" "mimir" {
  count = local.mimir_enabled ? 1 : 0

  vpc_id       = local.vpc_id
  service_name = var.mimir_endpoint_service_name
  # Only set service_region for a genuine cross-region endpoint. Passing it for a
  # same-region endpoint (the common case) is rejected by the EC2 API rather than
  # ignored. null omits it, giving a standard same-region interface endpoint.
  service_region     = local.mimir_is_cross_region ? local.mimir_service_region : null
  vpc_endpoint_type  = "Interface"
  subnet_ids         = local.mimir_is_cross_region ? local.private_subnet_ids : data.aws_subnets.mimir_compatible[0].ids
  security_group_ids = [aws_security_group.mimir_endpoint[0].id]

  # The name is a real public name, so private DNS from the endpoint service is
  # not what resolves it. The zone below does, which keeps the FQDN stable if the
  # service is ever migrated.
  private_dns_enabled = false

  tags = merge(
    {
      Name = "mimir-endpoint"
    },
    local.tags
  )

  lifecycle {
    # PrivateLink does not cross the commercial/gov partition boundary, so there is
    # no service name a gov VPC could point at here. Without this the failure is a
    # mid-apply "InvalidParameter: The input serviceRegion is invalid" from the EC2
    # API, which reads like a typo rather than the partition it actually is.
    precondition {
      condition     = !local.is_us_gov
      error_message = "enable_mimir is not supported on GovCloud: there is no PrivateLink path from the gov partition to the commercial Mimir. Leave enable_mimir unset - null resolves to false in this partition - or set it false explicitly. Gov envs still reach Mimir over the public gov ingest endpoint, which the logical layer's mimir_url now defaults to on its own; it does not need setting."
    }
  }
}

# A zone for exactly one FQDN, not a parent zone: it overrides that single name
# inside this VPC and leaves the rest of dozuki.dev resolving publicly. The
# public name has a blackhole record on purpose, so a VPC that loses this zone
# fails loudly instead of shipping metrics to whatever the legacy wildcard
# points at.
resource "aws_route53_zone" "mimir_private" {
  count = local.mimir_enabled ? 1 : 0

  name = var.mimir_ingest_fqdn

  vpc {
    vpc_id = local.vpc_id
  }

  tags = merge(
    {
      Name = "mimir-private-dns"
    },
    local.tags
  )
}

# Alias at the zone apex. A CNAME cannot live at an apex, and the interface
# endpoint exposes both halves an alias needs (dns_name and hosted_zone_id).
resource "aws_route53_record" "mimir" {
  count = local.mimir_enabled ? 1 : 0

  zone_id = aws_route53_zone.mimir_private[0].zone_id
  name    = var.mimir_ingest_fqdn
  type    = "A"

  alias {
    name                   = aws_vpc_endpoint.mimir[0].dns_entry[0]["dns_name"]
    zone_id                = aws_vpc_endpoint.mimir[0].dns_entry[0]["hosted_zone_id"]
    evaluate_target_health = false
  }
}
