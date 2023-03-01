# Set common variables for the environment. This is automatically pulled in in the root terragrunt.hcl configuration to
# feed forward to the child modules.
locals {
  #customer_id_parameters = {default: "/change/this", webhooks: "/change/this/also"}
  dms_setup = false
}