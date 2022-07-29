output "dashboard_url" {
  description = "URL to your Dozuki Dashboard."
  value       = format("https://%s:8800", var.nlb_dns_name)
}

output "dashboard_password" {
  description = "Password for your Dozuki Dashboard."
  value       = nonsensitive(random_password.dashboard_password.result)
}

output "dozuki_url" {
  description = "URL to your Dozuki Installation."
  value       = format("https://%s", var.nlb_dns_name)
}

output "grafana_admin_password" {
  description = "Password for Grafana admin user"
  value       = nonsensitive(try(random_password.grafana_admin[0].result, null))
}