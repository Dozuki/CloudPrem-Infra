# Set common variables for the environment. This is automatically pulled in in the root terragrunt.hcl configuration to
# feed forward to the child modules.
locals {
  environment = "full"
  enable_webhooks = true
  enable_bi = true
  rds_multi_az = true
  highly_available_nat_gateway = false
  dozuki_customer_id_parameter_name = "/dozuki/workstation/kots/webhooks/customer_id"
  protect_resources = false
  bi_public_access = true
  bi_access_cidrs = ["0.0.0.0/0"]
  grafana_access_cidrs = ["0.0.0.0/0"]
  bi_vpn_access = false
}
