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
  sensitive   = true
}

output "replicate_instructions" {
  value = var.s3_replicate_buckets ? "NOTE: Be sure to verify the Replicate Batch Operations complete successfully before deleting the donor buckets!" : null
}
output "ingress_ip" {
  description = "Public IP of the ingress load balancer (Azure only; point DNS here)."
  value       = var.cloud == "azure" ? try(kubernetes_service_v1.envoy_proxy_azure[0].status[0].load_balancer[0].ingress[0].ip, "") : ""
}
