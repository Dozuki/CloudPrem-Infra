output "cluster_id" {
  value       = aws_elasticache_cluster.this.id
  description = "Cluster ID"
}

output "security_group_id" {
  value       = join("", aws_security_group.this.*.id)
  description = "Security Group ID"
}

output "cluster_address" {
  value       = aws_elasticache_cluster.this.cluster_address
  description = "Cluster address"
}

output "cluster_configuration_endpoint" {
  value       = aws_elasticache_cluster.this.configuration_endpoint
  description = "Cluster configuration endpoint"
}

output "cluster_urls" {
  value       = null_resource.cluster_urls.*.triggers.name
  description = "Cluster URLs"
}

output "port" {
  value       = aws_elasticache_cluster.this.port
  description = "Cluster endpoint port"
}