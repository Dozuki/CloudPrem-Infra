output "dashboard_url" {
  description = "URL to your Dozuki Dashboard."
  value       = format("https://%s:8800", var.dns_domain_name)
}

output "dashboard_password" {
  description = "Password for your Dozuki Dashboard."
  value       = nonsensitive(random_password.dashboard_password.result)
}

output "dozuki_url" {
  description = "URL to your Dozuki Installation."
  value       = format("https://%s", var.dns_domain_name)
}

output "grafana_url" {
  value = local.grafana_url
}

output "grafana_admin_username" {
  value = local.grafana_admin_username
}

output "grafana_admin_password" {
  description = "Password for Grafana admin user"
  value       = local.grafana_admin_password
}

output "replicate_instructions" {
  value = var.s3_replicate_buckets ? "NOTE: Be sure to verify the Replicate Batch Operations complete successfully before deleting the donor buckets!" : null
}