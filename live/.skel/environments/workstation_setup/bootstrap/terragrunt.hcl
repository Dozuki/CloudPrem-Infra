# Was `skip = <cond>`, removed in terragrunt 0.8x. `exclude` is the replacement;
# actions = ["all"] reproduces the old all-commands behaviour exactly.
exclude {
  if      = get_env("SKIP_SETUP", true)
  actions = ["all"]
}
# Terragrunt will copy the Terraform configurations specified by the source parameter, along with any files in the
# working directory, into a temporary folder, and execute your Terraform commands in that folder.
terraform {
  source = "../../../../../terraform//bootstrap"
}

# Include all settings from the root terragrunt.hcl file
include "root" {
  path = find_in_parent_folders("root.hcl")
}