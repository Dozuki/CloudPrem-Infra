locals {
  # Automatically load environment-level variables
  environment_vars = read_terragrunt_config(find_in_parent_folders("env.hcl"))

  # Extract out common variables for reuse
  env = local.environment_vars.locals.environment
}

# Terragrunt will copy the Terraform configurations specified by the source parameter, along with any files in the
# working directory, into a temporary folder, and execute your Terraform commands in that folder.
terraform {
  source = "../../../../../cloudprem//compute"
}

# Include all settings from the root terragrunt.hcl file
include {
  path = find_in_parent_folders()
}
dependency "network" {
  config_path = "../network"

  mock_outputs = {
    vpc_id = "temporary-dummy-id"
  }
  mock_outputs_allowed_terraform_commands = ["validate"]
}

inputs = {
  vpc_id = dependency.network.outputs.vpc_id
}
