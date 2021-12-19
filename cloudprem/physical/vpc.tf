module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "3.11.0"

  count = local.create_vpc ? 1 : 0

  name = local.identifier
  cidr = var.vpc_cidr
  azs  = slice(data.aws_availability_zones.available.names, 0, 3)

  enable_nat_gateway   = true
  single_nat_gateway   = !var.highly_available_nat_gateway
  enable_dns_hostnames = true
  enable_dns_support   = true

  # VPC Flow Logs (Cloudwatch log group and IAM role will be created)
  enable_flow_log                      = true
  create_flow_log_cloudwatch_log_group = true
  create_flow_log_cloudwatch_iam_role  = true
  flow_log_max_aggregation_interval    = 60

  public_subnets = [for i in range(local.azs_count) : cidrsubnet(var.vpc_cidr, 4, i)]

  public_subnet_tags = {
    "kubernetes.io/cluster/${local.identifier}" = "shared"
    "kubernetes.io/role/elb"                    = "1"
    "type"                                      = "public"
  }

  private_subnets = [for i in range(local.azs_count, local.azs_count * 2) : cidrsubnet(var.vpc_cidr, 4, i)]

  private_subnet_tags = {
    "kubernetes.io/cluster/${local.identifier}" = "shared"
    "kubernetes.io/role/internal-elb"           = "1"
    "type"                                      = "private"
  }

  tags = local.tags
}