resource "aws_security_group_rule" "replicated_ui_access" {
  type              = "ingress"
  from_port         = 32001
  to_port           = 32001
  protocol          = "tcp"
  cidr_blocks       = var.replicated_ui_access_cidrs #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.worker_security_group_id
  description       = "Access to the replicated UI"
}

resource "aws_security_group_rule" "app_access_https" {
  type              = "ingress"
  from_port         = 32005
  to_port           = 32005
  protocol          = "tcp"
  cidr_blocks       = var.app_access_cidrs #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.worker_security_group_id
  description       = "Access to application"
}

resource "aws_security_group_rule" "app_access_http" {
  type              = "ingress"
  from_port         = 32010
  to_port           = 32010
  protocol          = "tcp"
  cidr_blocks       = var.app_access_cidrs #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.worker_security_group_id
  description       = "Access to application"
}

#tfsec:ignore:aws-elbv2-alb-not-public
module "nlb" {
  source  = "terraform-aws-modules/alb/aws"
  version = "6.6.1"

  name = local.identifier

  load_balancer_type = "network"
  internal           = !var.app_public_access

  vpc_id  = local.vpc_id
  subnets = local.public_subnet_ids

  target_groups = [
    {
      name_prefix      = "rep-"
      backend_protocol = "TCP"
      backend_port     = 32001
      target_type      = "instance"
    },
    {
      name_prefix      = "app-"
      backend_protocol = "TCP"
      backend_port     = 32005
      target_type      = "instance"
    },
    {
      name_prefix      = "http-"
      backend_protocol = "TCP"
      backend_port     = 32010
      target_type      = "instance"
    }
  ]

  http_tcp_listeners = [
    {
      port               = 8800
      protocol           = "TCP"
      target_group_index = 0
    },
    {
      port               = 443
      protocol           = "TCP"
      target_group_index = 1
    },
    {
      port               = 80
      protocol           = "TCP"
      target_group_index = 2
    }
  ]

  tags = local.tags
}