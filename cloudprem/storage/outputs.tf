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