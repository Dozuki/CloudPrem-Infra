# Set account-wide variables. These are automatically pulled in to configure the remote state bucket in the root
# terragrunt.hcl configuration.
locals {
  aws_account_id = "<ENTER AWS GOVCLOUD ACCT #>"
  aws_profile    = "gov"
}