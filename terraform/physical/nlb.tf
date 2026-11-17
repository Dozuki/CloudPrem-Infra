# The NLB reaches Envoy from its own ENIs in the public subnets, on the shifted
# ports only (10080, 10443). Envoy Gateway shifts 80/443 to 10080/10443 and the
# NLB listeners forward to those ports. Client IPs travel in the PROXY protocol
# v2 header, so these rules never see them and only need the subnet CIDRs.
resource "aws_security_group_rule" "nlb_to_envoy_https_shifted" {
  type              = "ingress"
  from_port         = 10443
  to_port           = 10443
  protocol          = "tcp"
  cidr_blocks       = local.public_subnet_cidrs
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "NLB to Envoy HTTPS shifted port (IP target)"

  lifecycle {
    create_before_destroy = true
    precondition {
      condition     = length(local.public_subnet_cidrs) > 0
      error_message = "No public subnets were found. A supplied vpc_id needs subnets tagged type = public."
    }
  }
}

resource "aws_security_group_rule" "nlb_to_envoy_http_shifted" {
  type              = "ingress"
  from_port         = 10080
  to_port           = 10080
  protocol          = "tcp"
  cidr_blocks       = local.public_subnet_cidrs
  security_group_id = module.eks_cluster.cluster_primary_security_group_id
  description       = "NLB to Envoy HTTP shifted port for ACME http01 challenges (IP target)"

  lifecycle {
    create_before_destroy = true
    precondition {
      condition     = length(local.public_subnet_cidrs) > 0
      error_message = "No public subnets were found. A supplied vpc_id needs subnets tagged type = public."
    }
  }
}

#tfsec:ignore:aws-elbv2-alb-not-public
module "nlb" {
  source  = "terraform-aws-modules/alb/aws"
  version = "~> 10.0"

  name = local.identifier

  load_balancer_type               = "network"
  internal                         = !var.app_public_access
  enable_cross_zone_load_balancing = true

  # The alb module (~> 10.0) defaults enable_deletion_protection=true (since
  # v9.0.0). Follow protect_resources like rds/aurora/bi so non-protected stacks
  # (e.g. the test harness) can be torn down without manually clearing it.
  enable_deletion_protection = var.protect_resources

  vpc_id  = local.vpc_id
  subnets = local.public_subnet_ids

  # Disable the module's security group — NLB uses the EKS cluster SG rules defined above.
  create_security_group = false

  # IP targeting: NLB sends directly to Envoy proxy pod IPs.
  # PROXY protocol v2 is required for real client IP delivery: with IP targets + TCP,
  # the NLB does NOT preserve the source IP natively — Envoy would see the NLB ENI IP.
  # proxy_protocol_v2=true instructs the NLB to prepend a PROXY v2 header with the
  # original client IP/port; Envoy's ClientTrafficPolicy (proxyProtocol mode) reads it.
  # Both target groups must send PROXY v2 because the Gateway-wide ClientTrafficPolicy
  # applies to all listeners (443 app + 80 ACME/redirect).
  target_groups = {
    app = {
      name_prefix       = "app-"
      protocol          = "TCP"
      port              = 443
      target_type       = "ip"
      create_attachment = false
      proxy_protocol_v2 = true

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
      proxy_protocol_v2 = true

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