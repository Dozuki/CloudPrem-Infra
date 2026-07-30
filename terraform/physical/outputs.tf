output "eks_cluster_id" {
  description = "EKS Cluster Name"
  value       = module.eks_cluster.cluster_name
}
output "eks_cluster_access_role_arn" {
  description = "IAM Role ARN for EKS cluster access"
  value       = module.cluster_access_role_assumable.arn
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

output "vpc_id" {
  description = "VPC ID"
  value       = local.vpc_id
}
output "dms_task_arn" {
  description = "DMS replication ARN for BI. Now a serverless replication config, not a task; the output name is kept so consumers do not have to change."
  value       = try(aws_dms_replication_config.this[0].arn, "")
}
output "dms_replication_generation" {
  description = "Short hash of the replication-config attributes whose change makes the provider stop the replication: the endpoint ARNs, the mappings and settings, and every compute_config field (subnet group, security groups, KMS key, multi-AZ, both DCU bounds). The logical dms-start Job folds this into its name, so the modify that leaves the replication stopped also forces a fresh Job to start it again; with a static name the Completed Job never re-ran and the replication stayed stopped against the 48h deprovision clock."
  # resourceReplicationConfigUpdate (provider v6.56.0) stops the replication on ANY change to
  # this resource, not just the DCU bounds, so the whole compute_config belongs here. A
  # bi_access_cidrs edit that replaces the BI security group is the easy one to miss: it stops
  # the replication and, without these fields, leaves the generation unmoved and no Job to
  # start it again.
  #
  # Endpoint ATTRIBUTES stay out on purpose - only the ARNs are here. A repoint (the rds ->
  # aurora BI switch, a cutover) modifies the endpoint in place, and a Job minted by that
  # would issue resume-processing against a target that needs reload-target. See bi.tf.
  value = try(substr(sha256(join(":", [
    aws_dms_replication_config.this[0].arn,
    aws_dms_replication_config.this[0].source_endpoint_arn,
    aws_dms_replication_config.this[0].target_endpoint_arn,
    sha256(aws_dms_replication_config.this[0].table_mappings),
    sha256(aws_dms_replication_config.this[0].replication_settings),
    aws_dms_replication_config.this[0].compute_config[0].replication_subnet_group_id,
    join(",", sort(tolist(aws_dms_replication_config.this[0].compute_config[0].vpc_security_group_ids))),
    aws_dms_replication_config.this[0].compute_config[0].kms_key_id,
    tostring(aws_dms_replication_config.this[0].compute_config[0].multi_az),
    tostring(var.bi_dms_min_dcu),
    tostring(var.bi_dms_max_dcu),
  ])), 0, 8), "")
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
  value       = module.nlb.dns_name
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
  value       = aws_route53_record.vault.fqdn
}
output "nlb_https_target_group_arn" {
  description = "NLB HTTPS target group ARN for TargetGroupBinding"
  value       = module.nlb.target_groups["app"].arn
}
output "nlb_http_target_group_arn" {
  description = "NLB HTTP target group ARN for TargetGroupBinding"
  value       = module.nlb.target_groups["acme"].arn
}

output "db_resource_id" {
  description = "Stable identifier of the primary database (Aurora cluster_resource_id / RDS db_instance_resource_id). Changes on every DB replace; the chart folds it into the migration Job name so a same-image DB replace re-runs migrations."
  value       = local.db_resource_id
}

output "dr_region" {
  description = "Region the DR replication layer targets (empty when DR disabled)."
  value       = var.enable_dr ? var.dr_region : ""
}

output "dr_s3_bucket_names" {
  description = "DR destination S3 bucket names by content type."
  value       = { for k, b in aws_s3_bucket.dr_guide_buckets : k => b.id }
}

output "dr_s3_kms_key_arn" {
  description = "ARN of the DR-region S3 KMS key (use as s3_kms_key_id when rebuilding in DR)."
  value       = try(aws_kms_key.dr_s3[0].arn, "")
}

output "dr_rds_backup_replication_arn" {
  description = "ARN of the replicated RDS automated backups in the DR region."
  value       = try(aws_db_instance_automated_backups_replication.primary[0].id, "")
}

output "aurora_migration_task_arn" {
  description = "ARN of the Aurora migration DMS task (empty when no migration is active). Consumed by the migration runner."
  value       = try(aws_dms_replication_task.aurora_migration[0].replication_task_arn, "")
}

output "aurora_migration_target_endpoint" {
  description = "Aurora cluster writer endpoint during a migration (empty otherwise). Runner/runbook convenience; the app only ever sees it through the credentials secret after cutover."
  value       = try(module.aurora[0].cluster_endpoint, "")
}

output "aurora_migration_credentials_secret" {
  description = "Secret ARN holding the Aurora migration target credentials (empty when no migration is active). Runner use only."
  value       = try(aws_secretsmanager_secret.aurora_migration_credentials[0].arn, "")
}
