output "primary_db_secret" {
  value = aws_secretsmanager_secret.primary_database_credentials.arn
}
output "guide_images_bucket" {
  value = local.guide_images_bucket
}
output "guide_objects_bucket" {
  value = local.guide_objects_bucket
}
output "guide_pdfs_bucket" {
  value = local.guide_pdfs_bucket
}
output "documents_bucket" {
  value = local.documents_bucket
}
output "memcached_cluster_address" {
  value = aws_elasticache_cluster.this.cluster_address
}
output "bi_database_credential_secret" {
  description = "If BI is enabled, this is the ARN to the AWS SecretsManager secret that contains the connection information for the BI database."
  value       = try(aws_secretsmanager_secret.replica_database[0].arn, null)
}