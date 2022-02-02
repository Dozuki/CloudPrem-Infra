# Set common variables for the environment. This is automatically pulled in in the root terragrunt.hcl configuration to
# feed forward to the child modules.
locals {
  environment = "full"
  enable_webhooks = true
  enable_bi = true
  rds_multi_az = true
  highly_available_nat_gateway = false
  dozuki_license_parameter_name = "/dozuki/workstation/alpha/license"
  protect_resources = false
}
