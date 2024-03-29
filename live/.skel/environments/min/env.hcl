# Set common variables for the environment. This is automatically pulled in in the root terragrunt.hcl configuration to
# feed forward to the child modules.
locals {
  environment = "min"
  enable_webhooks = false
  enable_bi = false
  rds_multi_az = false
  highly_available_nat_gateway = false
  dozuki_customer_id_parameter_name = "/dozuki/workstation/kots/default/customer_id"
  protect_resources = false
  alarm_email = "ddv@dozuki.com"
}
