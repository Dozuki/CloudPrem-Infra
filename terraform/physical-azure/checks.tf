# Tag-value format advisories. Warnings, not errors, on purpose: existing
# installs may carry legacy casing, and changing var.customer/var.environment
# on a live stack renames the identifier, which replaces resources. New stacks
# should use lowercase tokens so tag queries match across a fleet.
check "tag_value_format" {
  assert {
    condition     = var.environment == lower(var.environment)
    error_message = "environment '${var.environment}' is not lowercase; fleet tag queries on Environment expect lowercase tokens."
  }

  assert {
    condition     = var.customer == lower(var.customer)
    error_message = "customer '${var.customer}' is not lowercase; fleet tag queries on Customer expect lowercase tokens."
  }
}
