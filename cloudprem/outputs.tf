output "eks_cluster_access_role" {
  description = "AWS IAM role with full access to the Kubernetes cluster."
  value       = module.cluster_access_role.this_iam_role_arn
}

output "dashboard_url" {
  description = "URL to your Dozuki Dashboard."
  value       = "${module.nlb.this_lb_dns_name}:8800"
}

output "dozuki_url" {
  description = "URL to your Dozuki Installation."
  value       = module.nlb.this_lb_dns_name
}