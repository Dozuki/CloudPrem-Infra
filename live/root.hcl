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
}
provider "aws" {
  alias  = "dns"
  region = "${local.aws_region}"
  profile = "${local.aws_profile}"

  assume_role {
    role_arn = "${local.dns_role}"
  }
}
provider "aws" {
  alias               = "dr"
  region              = "${local.dr_region}"
  allowed_account_ids = ["${local.account_id}"]
  profile             = "${local.aws_profile}"
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
remote_state {
  backend = local.cloud == "azure" ? "azurerm" : "s3"
  config = merge(
    local.cloud == "azure" ? {
      resource_group_name  = local.az_state_rg
      storage_account_name = local.az_state_sa
      container_name       = "tfstate"
      subscription_id      = local.az_subscription_id
      tenant_id            = local.az_tenant_id
      use_oidc             = true
    } : {},
    local.cloud == "azure" ? {} : {
      encrypt        = true
      bucket         = "${get_env("TG_BUCKET_PREFIX", "")}dozuki-terraform-state-${local.aws_region}-${local.account_id}"
      region         = local.aws_region
      dynamodb_table = "dozuki-terraform-lock"
      profile        = local.aws_profile
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
)
