provider "aws" {
  alias  = "dns"
  region = data.aws_region.current.name

  assume_role {
    role_arn = local.route_53_role
  }
}

data "aws_route53_zone" "subdomain" {
  count    = var.external_fqdn == "" ? 1 : 0
  provider = aws.dns

  name = local.autogenerate_domain
}

resource "aws_route53_record" "subdomain" {
  count = var.external_fqdn == "" ? 1 : 0

  provider = aws.dns

  zone_id = data.aws_route53_zone.subdomain[0].zone_id
  name    = "${local.subdomain}.${data.aws_route53_zone.subdomain[0].name}"
  type    = "CNAME"
  ttl     = "300"
  records = [module.nlb.lb_dns_name]
}

resource "aws_route53_record" "subsite_subdomain" {
  count = var.external_fqdn == "" ? 1 : 0

  provider = aws.dns

  zone_id = data.aws_route53_zone.subdomain[0].zone_id
  name    = "*.${local.subdomain}.${data.aws_route53_zone.subdomain[0].name}"
  type    = "CNAME"
  ttl     = "300"
  records = [module.nlb.lb_dns_name]
}