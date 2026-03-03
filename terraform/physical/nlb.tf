# With IP targeting, NLB sends traffic directly to pod IPs on the private subnets.
# The NLB's ENIs live in the public subnets, so we allow their CIDRs to reach NGINX ports.
resource "aws_security_group_rule" "nlb_to_nginx_https" {
  type              = "ingress"
  from_port         = 443
  to_port           = 443
  protocol          = "tcp"
  cidr_blocks       = local.app_access_cidrs #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.node_security_group_id
  description       = "NLB to NGINX HTTPS (IP target)"
}

resource "aws_security_group_rule" "nlb_to_nginx_http" {
  type              = "ingress"
  from_port         = 80
  to_port           = 80
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"] #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.node_security_group_id
  description       = "NLB to NGINX HTTP for ACME http01 challenges (IP target)"
}

#tfsec:ignore:aws-elbv2-alb-not-public
module "nlb" {
  source  = "terraform-aws-modules/alb/aws"
  version = "8.4.0"

  name = local.identifier

  load_balancer_type               = "network"
  internal                         = !var.app_public_access
  enable_cross_zone_load_balancing = true

  vpc_id  = local.vpc_id
  subnets = local.public_subnet_ids

  # IP targeting: NLB sends directly to NGINX pod IPs.
  # No proxy protocol — NLB preserves client IP natively with IP targets.
  target_groups = [
    {
      name_prefix      = "app-"
      backend_protocol = "TCP"
      backend_port     = 443
      target_type      = "ip"
    },
    {
      name_prefix      = "acme-"
      backend_protocol = "TCP"
      backend_port     = 80
      target_type      = "ip"
    }
  ]

  http_tcp_listeners = [
    {
      port               = 443
      protocol           = "TCP"
      target_group_index = 0
    },
    {
      port               = 80
      protocol           = "TCP"
      target_group_index = 1
    }
  ]

  tags = local.tags
}