output "eks_cluster_access_role" {
  description = "AWS IAM role with full access to the Kubernetes cluster."
  value       = module.cluster_access_role.this_iam_role_arn
}

output "dashboard_url" {
  description = "URL to your Dozuki Dashboard."
  value       = format("https://%s:8800",module.nlb.this_lb_dns_name)
}

output "dashboard_password" {
  description = "Password for your Dozuki Dashboard."
  value = random_password.dashboard_password.result
}

output "dozuki_url" {
  description = "URL to your Dozuki Installation."
  value       = format("https://%s",module.nlb.this_lb_dns_name)
}