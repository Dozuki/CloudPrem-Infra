terraform {
  required_version = ">= 1.11.1"

  required_providers {
    aws = {
      source = "hashicorp/aws"
      # Plain range on purpose. The pin lives in .terraform.lock.hcl (committed since
      # #364), which is what actually decides the version; a constraint here is only a
      # bound. This briefly carried "!= 6.57.0" to dodge that release's request-signing
      # regression (hashicorp/terraform-provider-aws#49170), but Renovate's hashicorp
      # versioning does not implement "!=" - it logs "Unsupported hashicorp constraint"
      # and then skips the dependency entirely, which would leave the AWS provider
      # permanently unmanaged. That is a worse failure than the one it prevented, and
      # the lock file already prevents that one. The bad release is excluded in
      # renovate.json instead, where it is understood.
      version = "~> 6.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 3.0"
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
      source = "hashicorp/archive"
      # Pinned exactly: archive output bytes (and therefore the lambda
      # source_code_hash) can change between provider versions even for
      # identical source, which produces a perpetual phantom diff on the
      # lambda functions below. Pin so the computed hash is stable.
      version = "2.8.0"
    }
  }
}
provider "kubernetes" {
  host                   = module.eks_cluster.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks_cluster.cluster_certificate_authority_data)
  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", module.eks_cluster.cluster_name, "--region", data.aws_region.current.region]
  }
}

locals {
  identifier    = var.customer == "" ? "dozuki-${var.environment}" : "${var.customer}-${var.environment}"
  customer_name = var.subdomain_override != "" ? var.subdomain_override : var.customer == "" ? "dozuki" : var.customer

  # Database modifications: immediate, or deferred to preferred_maintenance_window.
  # Historically hardwired to !protect_resources, which silently makes every production DB
  # change a scheduled one - an apply that modifies the database returns green having only
  # QUEUED the change for sun:19:00-sun:23:00. That is fine for routine drift and actively
  # dangerous during a migration window, so db_apply_immediately can override it per env.
  # null keeps the historical behaviour exactly.
  db_apply_immediately = coalesce(var.db_apply_immediately, !var.protect_resources)

  # --EKS--
  create_eks_kms = var.eks_kms_key_id == "" ? true : false
  eks_kms_key    = local.create_eks_kms ? aws_kms_key.eks[0].arn : data.aws_kms_key.eks[0].arn

  # --Tags for all resources--
  // If you add a tag, it must never be blank.
  tags = merge(
    {
      Terraform = "true"
      # Service names the WORKLOAD, and this module is the managed private cloud product.
      # Not "cloudprem" - that term is deprecated. Not "<customer>-<env>" either: Customer and
      # Environment already carry that, and StackPath pins the exact unit, so a composite here
      # would be pure duplication. What Service uniquely buys is separating the MPC fleet from
      # shared infrastructure (vault) and from internal tooling (resource-reaper, airgap-hauler,
      # spacelift-private-worker, ...), which have no customer or environment at all.
      Service     = "mpc"
      Customer    = coalesce(var.customer, "dozuki")
      Environment = var.environment
      # Project and Identifier removed. Project was the constant "Dozuki" on every resource in
      # every account, so it grouped nothing; Identifier was a verbatim duplicate of Customer.
      # The tagging policy always had Project collapsing into Service. The keys stay ACTIVE as
      # cost allocation tags org-wide (other accounts may group on them), we just stop emitting.
    },
    var.delete_after != "" ? { deleteAfter = var.delete_after } : {},
  )

  is_us_gov = data.aws_partition.current.partition == "aws-us-gov"

  # --DNS--
  subdomain_parts = {
    "%CUSTOMER%"    = local.customer_name
    "%ENVIRONMENT%" = var.environment
    "%REGION%"      = data.aws_region.current.region
    "%ACCOUNT%"     = data.aws_caller_identity.current.account_id
  }
  subdomain = join("-", [for part in var.subdomain_format : local.subdomain_parts[part] if local.subdomain_parts[part] != ""])

  // What role should we allow our EKS worker nodes to assume to allow for cert-manager DNS challenges. (provider config is in root terragrunt.hcl)
  // We are unable to generate a subdomain on govcloud due to its restrictions but we still need a role to assume due to
  // terraform/terragrunt's native inability to conditionally create providers.
  route_53_role = local.is_us_gov ? "arn:aws-us-gov:iam::446787640263:role/Route53AccessRole" : "arn:aws:iam::010601635461:role/Route53AccessRole"

  // External FQDN takes precedence over everything else.
  // (if external_fqdn is specified then use it, else (if we are in govcloud then use the nlb dns name else use the autogenerated name))
  dns_domain_name = var.external_fqdn != "" ? var.external_fqdn : local.is_us_gov ? try(module.nlb.dns_name, "") : aws_route53_record.subdomain[0].name

  // We don't support autogenerated subdomains in govcloud due to its restrictions on dns zones.
  autogenerate_domain = var.managed_private_cloud ? local.is_us_gov ? "" : "dozuki.cloud" : ""

  # --Database--
  ca_cert_identifier = "rds-ca-rsa4096-g1"
  ca_cert_pem_file   = local.is_us_gov ? "vendor/us-gov-west-1-bundle.pem" : "vendor/global-bundle.pem"
  bi_subnet_ids      = var.bi_public_access ? local.public_subnet_ids : local.private_subnet_ids

  // If DMS is explicitly enabled for conditional replication purposes OR if public access is desired. (RDS RR is not appropriate for public access)
  // If true we will use an empty RDS instance and setup replication via DMS.
  // If false we will use an RDS Read Replica and let RDS manage the replication for us.
  dms_enabled = var.enable_bi ? (var.bi_dms_enabled || var.bi_public_access) : false

  # The DMS-target BI database runs on Aurora Serverless v2 unless the stack pins the legacy
  # provisioned-RDS path. Only meaningful when DMS is in play; the read-replica and
  # aurora-private-BI paths are unaffected.
  bi_uses_aurora = local.dms_enabled && var.bi_db_engine == "aurora"

  # BI connection facts, engine-agnostic. Three shapes collapse here:
  #   dms + aurora  -> the BI Aurora cluster's writer endpoint and its own master creds
  #   dms + rds     -> the provisioned DMS-target instance (legacy)
  #   no dms        -> an RDS read replica, or on aurora the primary's reader endpoint
  # Consumed by the DMS target endpoint and the BI credentials secret, so both follow whichever
  # engine is active without either having to know which.
  bi_db_host = var.enable_bi ? (
    local.bi_uses_aurora ? module.bi_aurora[0].cluster_endpoint :
    local.dms_enabled ? module.dms_replica_database[0].db_instance_address :
    (var.db_engine == "rds" ? module.rds_replica_database[0].db_instance_address : local.db_reader_endpoint)
  ) : ""

  bi_db_port = local.bi_uses_aurora ? module.bi_aurora[0].cluster_port : (
    local.dms_enabled ? module.dms_replica_database[0].db_instance_port : local.db_port
  )

  bi_db_username = local.bi_uses_aurora ? local.db_username : (
    local.dms_enabled ? module.dms_replica_database[0].db_instance_username : local.db_username
  )

  bi_db_password = local.bi_uses_aurora ? random_password.bi_aurora[0].result : (
    local.dms_enabled ? module.dms_replica_database[0].db_instance_password : local.db_password
  )

  bi_db_identifier = local.bi_uses_aurora ? module.bi_aurora[0].cluster_id : (
    local.dms_enabled ? module.dms_replica_database[0].db_instance_id : local.db_identifier
  )

  bi_db_resource_id = local.bi_uses_aurora ? module.bi_aurora[0].cluster_resource_id : (
    local.dms_enabled ? module.dms_replica_database[0].db_instance_resource_id : local.db_resource_id
  )

  # --Access Config--
  secure_default_bi_access_cidrs = length(var.bi_access_cidrs) == 0 ? [local.vpc_cidr] : var.bi_access_cidrs

  // If the secure default BI CIDRs computed above equals neither a default route (0.0.0.0/0) NOR the local VPC CIDR
  // then ensure the local VPC CIDR is included in the access list. This ensures that local VPC resources will always have
  // access even if the customer has a custom CIDR access list.
  bi_access_cidrs  = local.secure_default_bi_access_cidrs != tolist(["0.0.0.0/0"]) && local.secure_default_bi_access_cidrs != [local.vpc_cidr] ? concat([local.vpc_cidr], var.bi_access_cidrs) : local.secure_default_bi_access_cidrs
  app_access_cidrs = var.app_access_cidrs != tolist(["0.0.0.0/0"]) ? concat([local.vpc_cidr], var.app_access_cidrs) : var.app_access_cidrs

  # --S3 Buckets--
  // If all 4 guide buckets are specified we use them as a replication source.
  use_existing_buckets = length(var.s3_existing_buckets) == 4 ? true : false
  s3_kms_key_id        = aws_kms_key.s3.arn

  // We create this local to control creation of dynamic assets (you cannot use count *and* for_each in the same resource block)
  // The format of the s3_existing_buckets object is important and described in the variables.tf file.
  s3_existing_buckets = local.use_existing_buckets ? var.s3_existing_buckets : []

  // Do not change these values without modifying the `moved` blocks in s3.tf
  create_s3_bucket_names = ["image", "obj", "pdf", "doc"]

  // Build a list of maps of existing buckets with their prefix, source, and destination in this format:
  //{ type = one of local.create_s3_bucket_names, destination = arn of destination bucket for replication, source = arn of source bucket for replication }
  existing_bucket_map = local.use_existing_buckets ? [for _, bucket_type in local.create_s3_bucket_names : { type = bucket_type, destination = aws_s3_bucket.guide_buckets[bucket_type].arn, source = data.aws_s3_bucket.guide_buckets[bucket_type].bucket }] : []

  // Build lists for IAM policies to include all the source and destination buckets and objects.
  // Iterate the for_each map directly instead of one(flatten(toset(...[*]))). The splat+toset
  // round-trip converted the whole bucket object, which hoists the provider's deprecation marks
  // (request_payer, website_endpoint, website_domain, acceleration_status, ...) onto the result and
  // makes every plan print "Value derived from a deprecated source". Reading .arn off the map only
  // touches .arn, so no marks come along. Same output either way.
  s3_source_bucket_arn_list                   = local.use_existing_buckets ? [for _, bucket in data.aws_s3_bucket.guide_buckets : bucket.arn] : []
  s3_source_bucket_arn_list_with_objects      = local.use_existing_buckets ? [for _, bucket in data.aws_s3_bucket.guide_buckets : "${bucket.arn}/*"] : []
  s3_destination_bucket_arn_list_with_objects = [for _, bucket in aws_s3_bucket.guide_buckets : "${bucket.arn}/*"]

  // Conditional public access block to conform with unmanaged SCP
  s3_public_access_block_buckets = var.s3_block_public_access ? { for k, v in aws_s3_bucket.guide_buckets : k => v.id } : {}

  # --VPC--
  # Hardcoded 3. The old azs_count variable was never set to anything else in five
  # years, and it never actually worked as a knob: private subnet indices started AT
  # azs_count, so changing it renumbered (replaced) every private subnet, and values
  # above 3 silently round-robined onto the first three AZs anyway because the module
  # azs list was sliced to 3. Three is the right number for this product (EKS quorum,
  # Aurora spread, one NAT per AZ under HA). If per-region flexibility is ever really
  # needed, it requires an append-only subnet layout (private at fixed low indices,
  # public carved from the top), gated to new stacks - not a tunable variable.
  azs_count          = 3
  create_vpc         = var.vpc_id == "" ? true : false
  vpc_id             = local.create_vpc ? module.vpc[0].vpc_id : var.vpc_id
  vpc_cidr           = local.create_vpc ? module.vpc[0].vpc_cidr_block : data.aws_vpc.this[0].cidr_block
  public_subnet_ids  = local.create_vpc ? module.vpc[0].public_subnets : data.aws_subnets.public[0].ids
  private_subnet_ids = local.create_vpc ? module.vpc[0].private_subnets : data.aws_subnets.private[0].ids

  cf_template_version = var.cf_template_version
}

# Provider and global data resources
data "aws_partition" "current" {}
data "aws_availability_zones" "available" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {
  # Using a lifecycle precondition for compound variable validation
  lifecycle {
    precondition {
      condition     = local.slack_notifications_enabled || var.alarm_email != ""
      error_message = "${local.cf_template_version}Please configure either Slack or Email notifications via slack_bot_token + slack_channel_id (preferred), slack_webhook_url, or alarm_email. "
    }
  }
}
