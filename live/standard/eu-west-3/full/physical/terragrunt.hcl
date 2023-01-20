# Terragrunt will copy the Terraform configurations specified by the source parameter, along with any files in the
# working directory, into a temporary folder, and execute your Terraform commands in that folder.
terraform {
  source = "../../../../../terraform//physical"
}

# Include all settings from the root terragrunt.hcl file
include {
  path = find_in_parent_folders()
}
retryable_errors = [
  "(?s).*error waiting for Route in Route Table.*waiting for state to become 'ready'.*"
]

# These are the variables we have to pass in to use the module specified in the terragrunt configuration above
inputs = {

}

skip = get_env("SKIP_FULL", false)