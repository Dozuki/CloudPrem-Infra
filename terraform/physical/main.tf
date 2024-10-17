terraform {
  required_version = ">= 1.3.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 4.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.0"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.0"
    }
  }
}
provider "kubernetes" {
  host                   = data.aws_eks_cluster.main.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.main.certificate_authority[0].data)
  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    args        = ["eks", "get-token", "--cluster-name", data.aws_eks_cluster.main.name, "--region", data.aws_region.current.name, "--profile", var.aws_profile]
    command     = "aws"
  }
}

locals {
  identifier    = var.customer == "" ? "dozuki-${var.environment}" : "${var.customer}-${var.environment}"
  customer_name = var.subdomain_override != "" ? var.subdomain_override : var.customer == "" ? "dozuki" : var.customer

  # --EKS--
  cluster_access_role_name = "${local.identifier}-${data.aws_region.current.name}-cluster-access"
  create_eks_kms           = var.eks_kms_key_id == "" ? true : false
  eks_kms_key              = local.create_eks_kms ? aws_kms_key.eks[0].arn : data.aws_kms_key.eks[0].arn

  # --Tags for all resources--
  // If you add a tag, it must never be blank.
  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Identifier  = coalesce(var.customer, "Dozuki")
    Environment = var.environment
  }

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  # --DNS--
  subdomain_parts = {
    "%CUSTOMER%"    = local.customer_name
    "%ENVIRONMENT%" = var.environment
    "%REGION%"      = data.aws_region.current.name
    "%ACCOUNT%"     = data.aws_caller_identity.current.account_id
  }
  subdomain = join("-", [for part in var.subdomain_format : local.subdomain_parts[part] if local.subdomain_parts[part] != ""])

  // What role should we allow our EKS worker nodes to assume to allow for cert-manager DNS challenges. (provider config is in root terragrunt.hcl)
  // We are unable to generate a subdomain on govcloud due to its restrictions but we still need a role to assume due to
  // terraform/terragrunt's native inability to conditionally create providers.
  route_53_role = local.is_us_gov ? "arn:aws-us-gov:iam::446787640263:role/Route53AccessRole" : "arn:aws:iam::010601635461:role/Route53AccessRole"

  // External FQDN takes precedence over everything else.
  // (if external_fqdn is specified then use it, else (if we are in govcloud then use the nlb dns name else use the autogenerated name))
  dns_domain_name = var.external_fqdn != "" ? var.external_fqdn : local.is_us_gov ? try(module.nlb.lb_dns_name, "") : aws_route53_record.subdomain[0].name

  // We don't support autogenerated subdomains in govcloud due to its restrictions on dns zones.
  autogenerate_domain = var.managed_private_cloud ? local.is_us_gov ? "" : "dozuki.cloud" : ""

  # --Database--
  ca_cert_identifier = local.is_us_gov ? "rds-ca-rsa4096-g1" : "global-bundle"
  ca_cert_pem_file   = local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/global-bundle.pem"
  bi_subnet_ids      = var.bi_public_access ? local.public_subnet_ids : local.private_subnet_ids

  // If DMS is explicitly enabled for conditional replication purposes OR if public access is desired. (RDS RR is not appropriate for public access)
  // If true we will use an empty RDS instance and setup replication via DMS.
  // If false we will use an RDS Read Replica and let RDS manage the replication for us.
  dms_enabled = var.enable_bi ? (var.bi_dms_enabled || var.bi_public_access) : false
  bi_db       = var.enable_bi ? local.dms_enabled ? module.dms_replica_database[0] : module.rds_replica_database[0] : null

  // Static map of all supported database instance types and their memory allocation, used for Memory Usage alarm.
  // (Neither RDS nor CloudWatch provides a metric or a queryable resource for instance memory size)
  rds_instance_memory = {
    "db.m4.large"    = 8192
    "db.m4.xlarge"   = 16384
    "db.m4.2xlarge"  = 32768
    "db.m4.4xlarge"  = 65536
    "db.m4.10xlarge" = 163840
    "db.m5.large"    = 8192
    "db.m5.xlarge"   = 16384
    "db.m5.2xlarge"  = 32768
    "db.m5.4xlarge"  = 65536
    "db.m5.8xlarge"  = 131072
    "db.m5.12xlarge" = 196608
    "db.m5.16xlarge" = 262144
    "db.m5.24xlarge" = 393216
  }

  # --Access Config--
  secure_default_bi_access_cidrs = length(var.bi_access_cidrs) == 0 ? [local.vpc_cidr] : var.bi_access_cidrs

  // If the secure default BI CIDRs computed above equals neither a default route (0.0.0.0/0) NOR the local VPC CIDR
  // then ensure the local VPC CIDR is included in the access list. This ensures that local VPC resources will always have
  // access even if the customer has a custom CIDR access list.
  bi_access_cidrs            = local.secure_default_bi_access_cidrs != tolist(["0.0.0.0/0"]) && local.secure_default_bi_access_cidrs != [local.vpc_cidr] ? concat([local.vpc_cidr], var.bi_access_cidrs) : local.secure_default_bi_access_cidrs
  app_access_cidrs           = var.app_access_cidrs != tolist(["0.0.0.0/0"]) ? concat([local.vpc_cidr], var.app_access_cidrs) : var.app_access_cidrs
  replicated_ui_access_cidrs = var.replicated_ui_access_cidrs != tolist(["0.0.0.0/0"]) ? concat([local.vpc_cidr], var.replicated_ui_access_cidrs) : var.replicated_ui_access_cidrs

  # --S3 Buckets--
  // If all 4 guide buckets are specified we use them as a replication source.
  use_existing_buckets = length(var.s3_existing_buckets) == 4 ? true : false
  use_provided_s3_kms  = var.use_existing_s3_kms && var.s3_kms_key_id != "" ? true : false
  s3_kms_key_id        = local.use_provided_s3_kms ? var.s3_kms_key_id : aws_kms_key.s3[0].arn

  // We create this local to control creation of dynamic assets (you cannot use count *and* for_each in the same resource block)
  // The format of the s3_existing_buckets object is important and described in the variables.tf file.
  s3_existing_buckets = local.use_existing_buckets ? var.s3_existing_buckets : []

  // Do not change these values without modifying the `moved` blocks in s3.tf
  create_s3_bucket_names = ["image", "obj", "pdf", "doc"]

  // Build a list of maps of existing buckets with their prefix, source, and destination in this format:
  //{ type = one of local.create_s3_bucket_names, destination = arn of destination bucket for replication, source = arn of source bucket for replication }
  existing_bucket_map = local.use_existing_buckets ? [for _, bucket_type in local.create_s3_bucket_names : { type = bucket_type, destination = aws_s3_bucket.guide_buckets[bucket_type].arn, source = data.aws_s3_bucket.guide_buckets[bucket_type].bucket }] : []

  // Build lists for IAM policies to include all the source and destination buckets and objects
  s3_source_bucket_arn_list                   = local.use_existing_buckets ? [for _, bucket in one(flatten(toset(data.aws_s3_bucket.guide_buckets[*]))) : bucket.arn] : []
  s3_source_bucket_arn_list_with_objects      = local.use_existing_buckets ? [for _, bucket in one(flatten(toset(data.aws_s3_bucket.guide_buckets[*]))) : "${bucket.arn}/*"] : []
  s3_destination_bucket_arn_list_with_objects = [for _, bucket in one(flatten(toset(aws_s3_bucket.guide_buckets[*]))) : "${bucket.arn}/*"]

  // Conditional public access block to conform with unmanaged SCP
  s3_public_access_block_buckets = var.s3_block_public_access ? aws_s3_bucket.guide_buckets : {}

  # --VPC--
  azs_count          = var.azs_count
  create_vpc         = var.vpc_id == "" ? true : false
  vpc_id             = local.create_vpc ? module.vpc[0].vpc_id : var.vpc_id
  vpc_cidr           = local.create_vpc ? module.vpc[0].vpc_cidr_block : data.aws_vpc.this[0].cidr_block
  public_subnet_ids  = local.create_vpc ? module.vpc[0].public_subnets : data.aws_subnets.public[0].ids
  private_subnet_ids = local.create_vpc ? module.vpc[0].private_subnets : data.aws_subnets.private[0].ids
}

# Provider and global data resources
data "aws_eks_cluster" "main" {
  name = module.eks_cluster.cluster_id
}
data "aws_partition" "current" {}
data "aws_availability_zones" "available" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {
  # Using a lifecycle precondition for compound variable validation
  lifecycle {
    precondition {
      condition     = var.slack_webhook_url != "" || var.alarm_email != ""
      error_message = "Please configure either Slack or Email notifications via the slack_webhook_url or alarm_email variables. "
    }
  }
}
