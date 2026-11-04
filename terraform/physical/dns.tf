data "aws_route53_zone" "subdomain" {
  count    = local.autogenerate_domain != "" ? 1 : 0
  provider = aws.dns

  name = local.autogenerate_domain
}

resource "aws_route53_record" "subdomain" {
  count = local.autogenerate_domain != "" ? 1 : 0

  provider = aws.dns

  zone_id = data.aws_route53_zone.subdomain[0].zone_id
  name    = "${local.subdomain}.${data.aws_route53_zone.subdomain[0].name}"
  type    = "CNAME"
  ttl     = "300"
  records = [module.nlb.dns_name]
}

resource "aws_route53_record" "subsite_subdomain" {
  count = local.autogenerate_domain != "" ? 1 : 0

  provider = aws.dns

  zone_id = data.aws_route53_zone.subdomain[0].zone_id
  name    = "*.${local.subdomain}.${data.aws_route53_zone.subdomain[0].name}"
  type    = "CNAME"
  ttl     = "300"
  records = [module.nlb.dns_name]
}

locals {
  external_fqdn_records       = var.external_fqdn_route53_zone_id != "" && !var.external_fqdn_zone_in_env_account && !local.is_us_gov
  external_fqdn_local_records = var.external_fqdn_route53_zone_id != "" && var.external_fqdn_zone_in_env_account
}

resource "aws_route53_record" "external_fqdn" {
  count = local.external_fqdn_records ? 1 : 0

  provider = aws.dns

  zone_id = var.external_fqdn_route53_zone_id
  name    = var.external_fqdn
  type    = "CNAME"
  ttl     = "300"
  records = [module.nlb.dns_name]
}

# Ops hosts like grafana.<external_fqdn> resolve through the wildcard.
resource "aws_route53_record" "external_fqdn_wildcard" {
  count = local.external_fqdn_records ? 1 : 0

  provider = aws.dns

  zone_id = var.external_fqdn_route53_zone_id
  name    = "*.${var.external_fqdn}"
  type    = "CNAME"
  ttl     = "300"
  records = [module.nlb.dns_name]
}

# Same records again with the default provider, for a zone in this env's own account (aws.dns only reaches the shared org zones).
resource "aws_route53_record" "external_fqdn_local" {
  count = local.external_fqdn_local_records ? 1 : 0

  zone_id = var.external_fqdn_route53_zone_id
  name    = var.external_fqdn
  type    = "CNAME"
  ttl     = "300"
  records = [module.nlb.dns_name]
}

resource "aws_route53_record" "external_fqdn_local_wildcard" {
  count = local.external_fqdn_local_records ? 1 : 0

  zone_id = var.external_fqdn_route53_zone_id
  name    = "*.${var.external_fqdn}"
  type    = "CNAME"
  ttl     = "300"
  records = [module.nlb.dns_name]
}
