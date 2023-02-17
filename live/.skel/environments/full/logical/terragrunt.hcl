skip = get_env("SKIP_LOGICAL", false) || get_env("SKIP_INFRA", false) || get_env("SKIP_FULL", false)
# Terragrunt will copy the Terraform configurations specified by the source parameter, along with any files in the
# working directory, into a temporary folder, and execute your Terraform commands in that folder.
terraform {
  source = "../../../../../terraform//logical"
}

# Include all settings from the root terragrunt.hcl file
include "root" {
  path = find_in_parent_folders()
}

include "common" {
  path = find_in_parent_folders("common.hcl")
}