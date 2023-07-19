variable "customer_id_parameters" {
  type        = map(string)
  description = "Map of customer ID parameters to create. i.e. {default=\"abc123\", webhooks=\"def456\"}"
  default     = {}
}