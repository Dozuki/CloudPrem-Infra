output "msk_bootstrap_brokers" {
  value = try(replace(aws_msk_cluster.this[0].bootstrap_brokers, ",", "\\,"), "")
}
output "eks_worker_asg_arns" {
  value = module.eks_cluster.workers_asg_arns
}
output "eks_worker_asg_names" {
  value = module.eks_cluster.workers_asg_names
}
output "eks_cluster_id" {
  value = module.eks_cluster.cluster_id
}
output "eks_cluster_access_role_arn" {
  value = module.cluster_access_role_assumable.iam_role_arn
}
output "eks_oidc_cluster_access_role_name" {
  value = local.cluster_access_role_name
}
output "termination_handler_role_arn" {
  value = module.aws_node_termination_handler_role.iam_role_arn
}
output "termination_handler_sqs_queue_id" {
  value = module.aws_node_termination_handler_sqs.sqs_queue_id
}
output "nlb_dns_name" {
  value = module.nlb.lb_dns_name
}
output "cluster_primary_sg" {
  value = module.eks_cluster.cluster_primary_security_group_id
}
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
output "vpc_id" {
  description = "VPC ID"
  value       = local.vpc_id
}
output "azs_count" {
  value = local.azs_count
}
output "dms_task_arn" {
  value = try(aws_dms_replication_task.this[0].replication_task_arn, "")
}