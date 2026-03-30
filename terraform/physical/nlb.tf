# With IP targeting, NLB sends traffic directly to pod IPs on the private subnets.
# Envoy Gateway uses port shifting (443→10443, 80→10080) so we allow both the
# original and shifted ports through the cluster security group.
resource "aws_security_group_rule" "nlb_to_envoy_https" {
  type              = "ingress"
  from_port         = 443
  to_port           = 443
  protocol          = "tcp"
  cidr_blocks       = local.app_access_cidrs #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "NLB to Envoy HTTPS (IP target)"
}

resource "aws_security_group_rule" "nlb_to_envoy_https_shifted" {
  type              = "ingress"
  from_port         = 10443
  to_port           = 10443
  protocol          = "tcp"
  cidr_blocks       = local.app_access_cidrs #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "NLB to Envoy HTTPS shifted port (IP target)"
}

resource "aws_security_group_rule" "nlb_to_envoy_http" {
  type              = "ingress"
  from_port         = 80
  to_port           = 80
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"] #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "NLB to Envoy HTTP for ACME http01 challenges (IP target)"
}

resource "aws_security_group_rule" "nlb_to_envoy_http_shifted" {
  type              = "ingress"
  from_port         = 10080
  to_port           = 10080
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"] #tfsec:ignore:aws-vpc-no-public-ingress-sgr
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "NLB to Envoy HTTP shifted port for ACME http01 challenges (IP target)"
}

#tfsec:ignore:aws-elbv2-alb-not-public
module "nlb" {
  source  = "terraform-aws-modules/alb/aws"
  version = "~> 10.0"

  name = local.identifier

  load_balancer_type               = "network"
  internal                         = !var.app_public_access
  enable_cross_zone_load_balancing = true

  vpc_id  = local.vpc_id
  subnets = local.public_subnet_ids

  # Disable the module's security group — NLB uses the EKS cluster SG rules defined above.
  create_security_group = false

  # IP targeting: NLB sends directly to Envoy proxy pod IPs.
  # No proxy protocol — NLB preserves client IP natively with IP targets.
  target_groups = {
    app = {
      name_prefix       = "app-"
      protocol          = "TCP"
      port              = 443
      target_type       = "ip"
      create_attachment = false

      # Auto Mode's LB controller requires this tag on externally-created target
      # groups so its scoped session policy allows RegisterTargets/DeregisterTargets.
      tags = {
        "eks:eks-cluster-name" = module.eks_cluster.cluster_name
      }
    }
    acme = {
      name_prefix       = "acme-"
      protocol          = "TCP"
      port              = 80
      target_type       = "ip"
      create_attachment = false

      tags = {
        "eks:eks-cluster-name" = module.eks_cluster.cluster_name
      }
    }
  }

  listeners = {
    https = {
      port     = 443
      protocol = "TCP"
      forward = {
        target_group_key = "app"
      }
    }
    http = {
      port     = 80
      protocol = "TCP"
      forward = {
        target_group_key = "acme"
      }
    }
  }

  tags = local.tags
}