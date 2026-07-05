# This layer uses OpenTofu's `provider for_each` (e.g. `provider = azurerm.main[each.key]`
# in main.tf) so the in-module azurerm provider runs with zero instances on AWS. tflint's
# parser follows Terraform grammar and cannot evaluate an indexed provider reference, so its
# terraform_required_providers rule aborts with "Invalid expression" on that construct. Disable
# just that rule here; every other rule still runs. Re-enable if tflint gains OpenTofu
# provider-for_each support.
rule "terraform_required_providers" {
  enabled = false
}
