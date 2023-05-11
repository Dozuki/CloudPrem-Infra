locals {
  # Automatically load account-level variables
  account_vars = read_terragrunt_config(find_in_parent_folders("account.hcl"))

  # Automatically load region-level variables
  region_vars = read_terragrunt_config(find_in_parent_folders("region.hcl"))

  # Automatically load environment-level variables
  environment_vars = read_terragrunt_config(find_in_parent_folders("env.hcl"))

  # Extract the variables we need for easy access
  account_id   = get_env("TG_AWS_ACCT_ID", local.account_vars.locals.aws_account_id)
  aws_region   = get_env("TG_AWS_REGION", local.region_vars.locals.aws_region)
  aws_profile  = get_env("TG_AWS_PROFILE", local.account_vars.locals.aws_profile)

  dns_role = local.aws_region == "us-gov-west-1" ? "arn:aws-us-gov:iam::446787640263:role/Route53AccessRole" : "arn:aws:iam::010601635461:role/Route53AccessRole"
}

# Generate an AWS provider block
generate "provider" {
  path      = "provider.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<EOF
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
EOF
}

# Configure Terragrunt to automatically store tfstate files in an S3 bucket
remote_state {
  backend = "s3"
  config = {
    encrypt        = true
    bucket         = "${get_env("TG_BUCKET_PREFIX", "")}dozuki-terraform-state-${local.aws_region}-${local.account_id}"
    key            = "${get_env("TG_STATE_PREFIX", "")}${path_relative_to_include()}/terraform.tfstate"
    region         = local.aws_region
    dynamodb_table = "dozuki-terraform-lock"
    profile = local.aws_profile
  }
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