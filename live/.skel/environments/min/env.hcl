# Set common variables for the environment. This is automatically pulled in in the root terragrunt.hcl configuration to
# feed forward to the child modules.
locals {
  environment = "min"
  enable_bi = false
  rds_multi_az = false
  highly_available_nat_gateway = false
  protect_resources = false
  # 3-replica opensearch, matching prod. Fresh stacks bootstrap HA in one shot
  # (empty disks can take any count); the two extra pods per stack are accepted.
  # Existing singletons still step 1 -> 2 -> 3 per the HA runbook.
  opensearch_replicas = 3
  alarm_email = "ddv@dozuki.com"
  image_tag   = "CHANGE_ME"
  nextjs_tag  = "CHANGE_ME"
}
