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
  external_fqdn_records = var.external_fqdn_route53_zone_id != "" && !local.is_us_gov
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
