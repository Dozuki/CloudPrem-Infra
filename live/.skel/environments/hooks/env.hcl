# Set common variables for the environment. This is automatically pulled in in the root terragrunt.hcl configuration to
# feed forward to the child modules.
locals {
  environment = "hooks"
  enable_webhooks = true
  enable_bi = false
  rds_multi_az = false
  highly_available_nat_gateway = false
  protect_resources = false
  alarm_email = "ddv@dozuki.com"
  image_tag   = "CHANGE_ME"
  nextjs_tag  = "CHANGE_ME"
}
