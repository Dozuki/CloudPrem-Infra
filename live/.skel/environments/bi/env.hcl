# Set common variables for the environment. This is automatically pulled in in the root terragrunt.hcl configuration to
# feed forward to the child modules.
locals {
  environment = "bi"
  enable_webhooks = false
  enable_bi = true
  rds_multi_az = false
  highly_available_nat_gateway = false
  dozuki_customer_id_parameter_name = "/dozuki/workstation/kots/default/customer_id"
  protect_resources = false
  bi_public_access = false
  bi_access_cidrs = ["0.0.0.0/0"]
  bi_vpn_access = false
  grafana_access_cidrs = ["0.0.0.0/0"]
  alarm_email = "ddv@dozuki.com"
  bi_dms_enabled = false //will still use DMS if public access is enabled regardless of this setting
}
