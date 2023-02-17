variable "customer_id_parameters" {
  type        = map(string)
  description = "Map of customer ID parameters to create. i.e. {default=\"abc123\", webhooks=\"def456\"}"
  default     = {}
}
variable "dms_setup" {
  type        = bool
  default     = false
  description = "If true we will create the dms_vpc IAM role needed for fresh accounts to support BI"
}