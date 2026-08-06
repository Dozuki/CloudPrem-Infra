locals {
  # Automatically load account-level variables
  account_vars = read_terragrunt_config(find_in_parent_folders("account.hcl"))

  # Automatically load region-level variables
  region_vars = read_terragrunt_config(find_in_parent_folders("region.hcl"))

  # Automatically load environment-level variables
  environment_vars = read_terragrunt_config(find_in_parent_folders("env.hcl"))

  # Cloud discriminator — mirrors infra-live root.hcl so the harness stays a faithful
  # test of production wiring. AWS is the default (no `cloud` key); an Azure account.hcl
  # sets cloud="azure". AWS locals are try()-guarded so an Azure account.hcl evaluates
  # cleanly. NOTE: the harness has no Azure env yet (live/standard, live/gov are
  # generated; no azure partition), so the azure branch below is DORMANT — kept in sync
  # with prod per the "mirror general terragrunt config to live/" rule.
  cloud = try(local.account_vars.locals.cloud, "aws")

  # Extract the variables we need for easy access
  account_id  = get_env("TG_AWS_ACCT_ID", try(local.account_vars.locals.aws_account_id, ""))
  aws_region  = get_env("TG_AWS_REGION", try(local.region_vars.locals.aws_region, ""))
  aws_profile = get_env("TG_AWS_PROFILE", try(local.account_vars.locals.aws_profile, ""))

  # DR region for the generated aws.dr provider. physical/dr.tf references
  # provider = aws.dr statically (even with enable_dr=false), so the provider must
  # always exist or plan/destroy fails. Falls back to us-west-2 when DR is off.
  dr_region = get_env("TG_AWS_DR_REGION", try(local.environment_vars.locals.dr_region, "us-west-2"))

  dns_role = local.aws_region == "us-gov-west-1" ? "arn:aws-us-gov:iam::446787640263:role/Route53AccessRole" : "arn:aws:iam::010601635461:role/Route53AccessRole"

  # Fleet tagging via provider default_tags, mirroring infra-live root.hcl (the
  # 2026-07-15 rollout there was never mirrored here despite the "mirror shared
  # terragrunt config" rule). This matters beyond bookkeeping: physical resources
  # that pass no explicit tags (the guide/log S3 buckets, the headless DR Aurora
  # secondary) were left with NO tags at all, so the harness's deleteAfter TTL
  # never reached them - ResourceReaper flagged them for review daily and, worse,
  # its purge tier could never collect them if a smoke teardown failed.
  #
  # deleteAfter is included here (unlike infra-live, which has no ephemeral
  # stacks): the test harness writes delete_after into the generated env.hcl, and
  # only tag-carrying resources are ever purged. ManagedBy=manual per the tagging
  # policy executor enum - this harness is local terragrunt, not Spacelift.
  environment_tag = try(local.environment_vars.locals.environment, "dev")
  customer_tag    = try(local.environment_vars.locals.customer, "dozuki")
  delete_after    = try(local.environment_vars.locals.delete_after, "")

  default_tags_tf = <<-EOF
      default_tags {
        tags = {
          Environment = "${local.environment_tag}"
          Customer    = "${local.customer_tag}"
          ManagedBy   = "manual"
          StackPath   = "${path_relative_to_include()}"
          Repo        = "CloudPrem-Infra"
          %{if local.delete_after != ""}deleteAfter = "${local.delete_after}"%{endif}
        }
      }
  EOF

  # Azure scalars (account.hcl provides these when cloud=="azure"; empty otherwise).
  az_subscription_id = try(local.account_vars.locals.subscription_id, "")
  az_tenant_id       = try(local.account_vars.locals.tenant_id, "")
  az_environment     = try(local.account_vars.locals.azure_environment, "public")
  az_state_rg        = try(local.account_vars.locals.state_resource_group, "")
  az_state_sa        = try(local.account_vars.locals.state_storage_account, "")

  # Generated provider.tf, per cloud. AWS: the real aws providers. Azure: an inert aws
  # stub for the cloud-agnostic logical module's count=0 aws refs (azurerm is in-module).
  aws_provider_tf = <<EOF
provider "aws" {
  region = "${local.aws_region}"
  # Only these AWS Account IDs may be operated on by this template
  allowed_account_ids = ["${local.account_id}"]
  profile = "${local.aws_profile}"
${local.default_tags_tf}
}
provider "aws" {
  alias  = "dns"
  region = "${local.aws_region}"
  profile = "${local.aws_profile}"
${local.default_tags_tf}
  assume_role {
    role_arn = "${local.dns_role}"
  }
}
provider "aws" {
  alias               = "dr"
  region              = "${local.dr_region}"
  allowed_account_ids = ["${local.account_id}"]
  profile             = "${local.aws_profile}"
${local.default_tags_tf}
}
EOF

  azure_provider_tf = <<EOF
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "deploy-stub"
  secret_key                  = "deploy-stub"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}
EOF
}

# Generate the provider block (AWS providers, or the inert aws stub on Azure)
generate "provider" {
  path      = "provider.tf"
  if_exists = "overwrite_terragrunt"
  contents  = local.cloud == "azure" ? local.azure_provider_tf : local.aws_provider_tf
}

# Configure Terragrunt to store tfstate in S3 (AWS) or an Azure Storage account (Azure).
# Conditional spreads (cond ? {...} : {}) keep this one map — a plain ternary of the
# two different-keyed configs would fail HCL type-checking.
#
# String- and bool-valued keys are split into SEPARATE spreads on purpose: a
# conditional map literal unifies to one element type, so mixing strings and
# bools in a single branch coerces the bools to strings ("true"). Terraform's
# backend schema then rejects them — s3 'encrypt' and azurerm 'use_oidc' /
# 'use_azuread_auth' must be real bools. merge() recombines the type-pure
# spreads into one object preserving each key's type.
remote_state {
  backend = local.cloud == "azure" ? "azurerm" : "s3"
  config = merge(
    # Azure (azurerm) — strings, then bools.
    local.cloud == "azure" ? {
      resource_group_name  = local.az_state_rg
      storage_account_name = local.az_state_sa
      container_name       = "tfstate"
      subscription_id      = local.az_subscription_id
      tenant_id            = local.az_tenant_id
    } : {},
    local.cloud == "azure" ? {
      use_oidc = true
      # Entra (AAD) data-plane auth — the state account has shared-key access
      # disabled (azure-bootstrap), so blob ops use the deployer SP's Storage
      # Blob Data Contributor role rather than an account key.
      use_azuread_auth = true
    } : {},
    # AWS (s3) — strings, then bool.
    local.cloud == "azure" ? {} : {
      bucket         = "${get_env("TG_BUCKET_PREFIX", "")}dozuki-terraform-state-${local.aws_region}-${local.account_id}"
      region         = local.aws_region
      dynamodb_table = "dozuki-terraform-lock"
      profile        = local.aws_profile
    },
    local.cloud == "azure" ? {} : {
      encrypt = true
    },
    { key = "${get_env("TG_STATE_PREFIX", "")}${path_relative_to_include()}/terraform.tfstate" },
  )
  generate = {
    path      = "backend.tf"
    if_exists = "overwrite_terragrunt"
  }
}


# ---------------------------------------------------------------------------------------------------------------------
# GLOBAL PARAMETERS
# These variables apply to all configurations in this subfolder. These are automatically merged into the child
# `terragrunt.hcl` config via the include block.
# ---------------------------------------------------------------------------------------------------------------------

# Configure root level variables that all resources can inherit. This is especially helpful with multi-account configs
# where terraform_remote_state data sources are placed directly into the modules.
inputs = merge(
  local.account_vars.locals,
  local.region_vars.locals,
  local.environment_vars.locals,
  # aws_profile must be the RESOLVED local, not account.hcl's literal. The locals above
  # honor a TG_AWS_PROFILE override (the harness sets it to its chained-role profile) but
  # the raw account_vars merge would forward the workstation default ("default") into
  # var.aws_profile, and physical's DR backfill local-exec exports that name to the aws
  # CLI, which fails hard in any environment where the profile does not exist.
  { aws_profile = local.aws_profile },
)
