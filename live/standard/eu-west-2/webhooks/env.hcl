# Set common variables for the environment. This is automatically pulled in in the root terragrunt.hcl configuration to
# feed forward to the child modules.
locals {
  environment = "hooks"
  enable_webhooks = true
  enable_bi = false
  rds_multi_az = false
  dozuki_license_parameter_name = "/dozuki/webhooks/license"
  protect_resources = false
}
