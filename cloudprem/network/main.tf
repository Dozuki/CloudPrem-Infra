terraform {
  required_providers {
    aws = "3.56.0"
  }
}

locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-dozuki-${var.environment}"

  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Identifier  = var.identifier
    Environment = var.environment
  }

  azs_count          = var.azs_count
  create_vpc         = var.vpc_id == "" ? true : false
  vpc_id             = local.create_vpc ? module.vpc[0].vpc_id : var.vpc_id
  vpc_cidr           = local.create_vpc ? module.vpc[0].vpc_cidr_block : data.aws_vpc.this[0].cidr_block
  public_subnet_ids  = local.create_vpc ? module.vpc[0].public_subnets : data.aws_subnet_ids.public
  private_subnet_ids = local.create_vpc ? module.vpc[0].private_subnets : data.aws_subnet_ids.private
}

data "aws_partition" "current" {}

data "aws_availability_zones" "available" {}

data "aws_caller_identity" "current" {}

data "aws_vpc" "this" {
  count = local.create_vpc ? 0 : 1
  id    = var.vpc_id
}

data "aws_subnet_ids" "public" {
  count = local.create_vpc ? 0 : 1

  vpc_id = var.vpc_id

  tags = {
    type = "public"
  }
}

data "aws_subnet_ids" "private" {
  count = local.create_vpc ? 0 : 1

  vpc_id = var.vpc_id

  tags = {
    type = "private"
  }
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "3.7.0"

  count = local.create_vpc ? 1 : 0

  name = local.identifier
  cidr = var.vpc_cidr
  azs  = slice(data.aws_availability_zones.available.names, 0, 3)

  enable_nat_gateway = true
  //  single_nat_gateway     = !var.highly_available_nat_gateway
  //  one_nat_gateway_per_az = true
  enable_dns_hostnames = true
  enable_dns_support   = true
  //  enable_vpn_gateway = true
  //  create_egress_only_igw = true

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
#tfsec:ignore:aws-vpc-no-public-egress-sgr
//module "endpoint_sg" {
//  count = local.create_vpc ? 1 : 0
//
//  source  = "terraform-aws-modules/security-group/aws"
//  version = "4.3.0"
//
//  name            = "${local.identifier}-endpoints"
//  use_name_prefix = false
//  description     = "Security group for VPC Endpoints."
//  vpc_id          = local.vpc_id
//
//  ingress_cidr_blocks = [local.vpc_cidr]
//  ingress_rules       = ["all-tcp"]
//
//  egress_rules = ["all-tcp"]
//
//  tags = local.tags
//}

//module "vpc_endpoints" {
//  count = local.create_vpc ? 1 : 0
//
//  source = "terraform-aws-modules/vpc/aws//modules/vpc-endpoints"
//
//  vpc_id             = local.vpc_id
//  security_group_ids = [module.endpoint_sg[0].security_group_id]
//
//  endpoints = {
//    s3 = {
//      service = "s3"
//      tags    = { Name = "s3-vpc-endpoint" }
//    },
////    dynamodb = {
////      service         = "dynamodb"
////      service_type    = "Gateway"
////      route_table_ids = flatten([module.vpc.intra_route_table_ids, module.vpc.private_route_table_ids, module.vpc.public_route_table_ids])
////      policy          = data.aws_iam_policy_document.dynamodb_endpoint_policy.json
////      tags            = { Name = "dynamodb-vpc-endpoint" }
////    },
//    ssm = {
//      service             = "ssm"
//      private_dns_enabled = true
//      subnet_ids          = local.private_subnet_ids
//    },
//    ssmmessages = {
//      service             = "ssmmessages"
//      private_dns_enabled = true
//      subnet_ids          = local.private_subnet_ids
//    },
////    lambda = {
////      service             = "lambda"
////      private_dns_enabled = true
////      subnet_ids          = module.vpc.private_subnets
////    },
////    ecs = {
////      service             = "ecs"
////      private_dns_enabled = true
////      subnet_ids          = module.vpc.private_subnets
////    },
////    ecs_telemetry = {
////      service             = "ecs-telemetry"
////      private_dns_enabled = true
////      subnet_ids          = module.vpc.private_subnets
////    },
//    ec2 = {
//      service             = "ec2"
//      private_dns_enabled = true
//      subnet_ids          = local.private_subnet_ids
//    },
//    ec2messages = {
//      service             = "ec2messages"
//      private_dns_enabled = true
//      subnet_ids          = local.private_subnet_ids
//    },
////    ecr_api = {
////      service             = "ecr.api"
////      private_dns_enabled = true
////      subnet_ids          = module.vpc.private_subnets
////      policy              = data.aws_iam_policy_document.generic_endpoint_policy.json
////    },
////    ecr_dkr = {
////      service             = "ecr.dkr"
////      private_dns_enabled = true
////      subnet_ids          = module.vpc.private_subnets
////      policy              = data.aws_iam_policy_document.generic_endpoint_policy.json
////    },
//    kms = {
//      service             = "kms"
//      private_dns_enabled = true
//      subnet_ids          = local.private_subnet_ids
//    },
////    dms = {
////      service             = "dms"
////      private_dns_enabled = true
////      subnet_ids          = local.private_subnet_ids
////    },
//    rds = {
//      service             = "rds"
//      private_dns_enabled = true
//      subnet_ids          = local.private_subnet_ids
//    },
//    sns = {
//      service             = "sns"
//      private_dns_enabled = true
//      subnet_ids          = local.private_subnet_ids
//    },
//    cloudtrail = {
//      service             = "cloudtrail"
//      private_dns_enabled = true
//      subnet_ids          = local.private_subnet_ids
//    },
////    codedeploy = {
////      service             = "codedeploy"
////      private_dns_enabled = true
////      subnet_ids          = module.vpc.private_subnets
////    },
////    codedeploy_commands_secure = {
////      service             = "codedeploy-commands-secure"
////      private_dns_enabled = true
////      subnet_ids          = module.vpc.private_subnets
////    },
//  }
//
//  tags = merge(local.tags, {
//    Project  = "Secret"
//    Endpoint = "true"
//  })
//}