# Was `skip = <cond>`, removed in terragrunt 0.8x. `exclude` is the replacement;
# actions = ["all"] reproduces the old all-commands behaviour exactly.
exclude {
  if      = get_env("SKIP_INFRA", false) || get_env("SKIP_FULL", false)
  actions = ["all"]
}
# Terragrunt will copy the Terraform configurations specified by the source parameter, along with any files in the
# working directory, into a temporary folder, and execute your Terraform commands in that folder.
terraform {
  source = "../../../../../terraform//physical"
}

# Include all settings from the root terragrunt.hcl file
include "root" {
  path = find_in_parent_folders("root.hcl")
}
# Was a bare `retryable_errors` list, removed in terragrunt 0.8x. The retry block
# now carries the patterns; max_attempts/sleep_interval_sec keep terragrunt's
# former defaults so behaviour is unchanged.
errors {
  retry "aws_eventual_consistency" {
    retryable_errors = [
      "(?s).*error waiting for Route in Route Table.*waiting for state to become 'ready'.*"
    ]
    max_attempts       = 3
    sleep_interval_sec = 5
  }
}
# These are the variables we have to pass in to use the module specified in the terragrunt configuration above
inputs = {

}