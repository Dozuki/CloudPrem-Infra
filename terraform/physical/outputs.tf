output "msk_bootstrap_brokers" {
  description = "Kafka bootstrap broker list"
  value       = try(aws_msk_cluster.this[0].bootstrap_brokers, "")
}
output "eks_cluster_id" {
  description = "EKS Cluster Name"
  value       = module.eks_cluster.cluster_name
}
output "eks_cluster_access_role_arn" {
  description = "IAM Role ARN for EKS cluster access"
  value       = module.cluster_access_role_assumable.iam_role_arn
}
output "dns_domain_name" {
  description = "URL to deployed application"
  value       = local.dns_domain_name
}
output "cluster_primary_sg" {
  description = "Primary security group for EKS cluster"
  value       = module.eks_cluster.cluster_primary_security_group_id
}
output "primary_db_secret" {
  description = "Secretmanager ARN to MySQL credential storage"
  value       = aws_secretsmanager_secret.primary_database_credentials.arn
}
output "guide_images_bucket" {
  value = lookup(aws_s3_bucket.guide_buckets["image"], "bucket", null)
}
output "guide_objects_bucket" {
  value = lookup(aws_s3_bucket.guide_buckets["obj"], "bucket", null)
}
output "guide_pdfs_bucket" {
  value = lookup(aws_s3_bucket.guide_buckets["pdf"], "bucket", null)
}
output "documents_bucket" {
  value = lookup(aws_s3_bucket.guide_buckets["doc"], "bucket", null)
}
output "s3_kms_key_id" {
  value = local.s3_kms_key_id
}
output "s3_replicate_buckets" {
  value = local.use_existing_buckets
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
  description = "DMS Replication Task ARN for BI"
  value       = try(aws_dms_replication_task.this[0].replication_task_arn, "")
}
output "bi_database_credential_secret" {
  description = "If BI is enabled, this is the ARN to the AWS SecretsManager secret that contains the connection information for the BI database."
  value       = try(aws_secretsmanager_secret.replica_database_credentials[0].arn, "")
}
output "bi_vpn_configuration_bucket" {
  description = "If BI is enabled, this is the S3 bucket that stores the OpenVPN configuration files for clients to connect to the BI database from the internet."
  value       = try(module.vpn[0].aws_vpn_configuration_bucket, "")
}
output "bastion_asg_name" {
  value = module.bastion.autoscaling_group_name
}
output "nlb_dns_name" {
  description = "The FQDN of the NLB."
  value       = module.nlb.lb_dns_name
}
output "dms_enabled" {
  description = "Whether DMS was enabled or not via combination of other input variables or directly"
  value       = local.dms_enabled
}
output "eks_oidc_issuer_url" {
  description = "OIDC issuer URL for the EKS cluster"
  value       = module.eks_cluster.cluster_oidc_issuer_url
}
output "private_subnet_ids" {
  description = "Private subnet IDs for the VPC"
  value       = local.private_subnet_ids
}
output "vault_endpoint_dns" {
  description = "Private DNS name for reaching Vault via PrivateLink"
  value       = try(aws_route53_record.vault[0].fqdn, "")
}
output "nlb_https_target_group_arn" {
  description = "NLB HTTPS target group ARN for TargetGroupBinding"
  value       = module.nlb.target_group_arns[0]
}
output "nlb_http_target_group_arn" {
  description = "NLB HTTP target group ARN for TargetGroupBinding"
  value       = module.nlb.target_group_arns[1]
}
