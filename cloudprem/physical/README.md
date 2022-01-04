<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | 3.70.0 |
| <a name="requirement_random"></a> [random](#requirement\_random) | 3.1.0 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | 3.70.0 |
| <a name="provider_null"></a> [null](#provider\_null) | n/a |
| <a name="provider_random"></a> [random](#provider\_random) | 3.1.0 |

## Modules

| Name | Source | Version |
|------|--------|---------|
| <a name="module_aws_node_termination_handler_role"></a> [aws\_node\_termination\_handler\_role](#module\_aws\_node\_termination\_handler\_role) | terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc | 4.7.0 |
| <a name="module_aws_node_termination_handler_sqs"></a> [aws\_node\_termination\_handler\_sqs](#module\_aws\_node\_termination\_handler\_sqs) | terraform-aws-modules/sqs/aws | ~> 3.1.0 |
| <a name="module_bastion"></a> [bastion](#module\_bastion) | terraform-aws-modules/autoscaling/aws | 3.8.0 |
| <a name="module_bastion_role"></a> [bastion\_role](#module\_bastion\_role) | terraform-aws-modules/iam/aws//modules/iam-assumable-role | 4.7.0 |
| <a name="module_bastion_sg"></a> [bastion\_sg](#module\_bastion\_sg) | terraform-aws-modules/security-group/aws | 4.7.0 |
| <a name="module_cluster_access_role"></a> [cluster\_access\_role](#module\_cluster\_access\_role) | terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc | 4.7.0 |
| <a name="module_cluster_access_role_assumable"></a> [cluster\_access\_role\_assumable](#module\_cluster\_access\_role\_assumable) | terraform-aws-modules/iam/aws//modules/iam-assumable-role | 4.7.0 |
| <a name="module_cpu_alarm"></a> [cpu\_alarm](#module\_cpu\_alarm) | terraform-aws-modules/cloudwatch/aws//modules/metric-alarm | 2.1.0 |
| <a name="module_database_sg"></a> [database\_sg](#module\_database\_sg) | terraform-aws-modules/security-group/aws | 4.7.0 |
| <a name="module_eks_cluster"></a> [eks\_cluster](#module\_eks\_cluster) | terraform-aws-modules/eks/aws | 17.24.0 |
| <a name="module_memory_alarm"></a> [memory\_alarm](#module\_memory\_alarm) | terraform-aws-modules/cloudwatch/aws//modules/metric-alarm | 2.1.0 |
| <a name="module_nlb"></a> [nlb](#module\_nlb) | terraform-aws-modules/alb/aws | 6.6.1 |
| <a name="module_nodes_alarm"></a> [nodes\_alarm](#module\_nodes\_alarm) | terraform-aws-modules/cloudwatch/aws//modules/metric-alarm | 2.1.0 |
| <a name="module_primary_database"></a> [primary\_database](#module\_primary\_database) | terraform-aws-modules/rds/aws | 3.4.1 |
| <a name="module_replica_database"></a> [replica\_database](#module\_replica\_database) | terraform-aws-modules/rds/aws | 3.4.1 |
| <a name="module_sns"></a> [sns](#module\_sns) | terraform-aws-modules/sns/aws | 2.1.0 |
| <a name="module_status_alarm"></a> [status\_alarm](#module\_status\_alarm) | terraform-aws-modules/cloudwatch/aws//modules/metric-alarm | 2.1.0 |
| <a name="module_vpc"></a> [vpc](#module\_vpc) | terraform-aws-modules/vpc/aws | 3.11.0 |

## Resources

| Name | Type |
|------|------|
| [aws_cloudwatch_event_rule.aws_node_termination_handler_asg](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/cloudwatch_event_rule) | resource |
| [aws_cloudwatch_event_rule.aws_node_termination_handler_spot](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/cloudwatch_event_rule) | resource |
| [aws_cloudwatch_event_target.aws_node_termination_handler_asg](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/cloudwatch_event_target) | resource |
| [aws_cloudwatch_event_target.aws_node_termination_handler_spot](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/cloudwatch_event_target) | resource |
| [aws_dms_certificate.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/dms_certificate) | resource |
| [aws_dms_endpoint.source](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/dms_endpoint) | resource |
| [aws_dms_endpoint.target](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/dms_endpoint) | resource |
| [aws_dms_replication_instance.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/dms_replication_instance) | resource |
| [aws_dms_replication_subnet_group.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/dms_replication_subnet_group) | resource |
| [aws_dms_replication_task.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/dms_replication_task) | resource |
| [aws_elasticache_cluster.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/elasticache_cluster) | resource |
| [aws_elasticache_parameter_group.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/elasticache_parameter_group) | resource |
| [aws_elasticache_subnet_group.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/elasticache_subnet_group) | resource |
| [aws_iam_policy.aws_node_termination_handler](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/iam_policy) | resource |
| [aws_iam_policy.cluster_access](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/iam_policy) | resource |
| [aws_iam_policy.cluster_autoscaler_policy](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/iam_policy) | resource |
| [aws_iam_policy.eks_worker](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/iam_policy) | resource |
| [aws_kms_key.bi](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/kms_key) | resource |
| [aws_kms_key.eks](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/kms_key) | resource |
| [aws_msk_cluster.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/msk_cluster) | resource |
| [aws_msk_configuration.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/msk_configuration) | resource |
| [aws_s3_bucket.guide_documents](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket.guide_images](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket.guide_objects](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket.guide_pdfs](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket.logging_bucket](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket_public_access_block.guide_documents_acl_block](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_public_access_block.guide_images_acl_block](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_public_access_block.guide_objects_acl_block](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_public_access_block.guide_pdfs_acl_block](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_public_access_block.logging_bucket_acl_block](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_secretsmanager_secret.primary_database_credentials](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/secretsmanager_secret) | resource |
| [aws_secretsmanager_secret.replica_database](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/secretsmanager_secret) | resource |
| [aws_secretsmanager_secret_version.primary_database_credentials](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/secretsmanager_secret_version) | resource |
| [aws_secretsmanager_secret_version.replica_database](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/secretsmanager_secret_version) | resource |
| [aws_security_group.elasticache](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group) | resource |
| [aws_security_group.kafka](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group) | resource |
| [aws_security_group_rule.app_access_http](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.app_access_https](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.egress](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.ingress_cidr_blocks](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.kafka_egress](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.kafka_ingress_cidr_blocks](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.kafka_ingress_security_groups](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.replicated_ui_access](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/security_group_rule) | resource |
| [null_resource.cluster_urls](https://registry.terraform.io/providers/hashicorp/null/latest/docs/resources/resource) | resource |
| [null_resource.replication_control](https://registry.terraform.io/providers/hashicorp/null/latest/docs/resources/resource) | resource |
| [random_password.primary_database](https://registry.terraform.io/providers/hashicorp/random/3.1.0/docs/resources/password) | resource |
| [random_password.replica_database](https://registry.terraform.io/providers/hashicorp/random/3.1.0/docs/resources/password) | resource |
| [aws_ami.amazon_linux_2](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/ami) | data source |
| [aws_availability_zones.available](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/availability_zones) | data source |
| [aws_caller_identity.current](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/caller_identity) | data source |
| [aws_eks_cluster.main](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/eks_cluster) | data source |
| [aws_eks_cluster_auth.main](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/eks_cluster_auth) | data source |
| [aws_iam_policy_document.aws_node_termination_handler](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.aws_node_termination_handler_events](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.cluster_access](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.cluster_autoscaler_pd](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.eks_worker](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/iam_policy_document) | data source |
| [aws_kms_key.eks](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/kms_key) | data source |
| [aws_kms_key.rds](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/kms_key) | data source |
| [aws_kms_key.s3](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/kms_key) | data source |
| [aws_partition.current](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/partition) | data source |
| [aws_region.current](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/region) | data source |
| [aws_s3_bucket.documents](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/s3_bucket) | data source |
| [aws_s3_bucket.guide_images](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/s3_bucket) | data source |
| [aws_s3_bucket.guide_objects](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/s3_bucket) | data source |
| [aws_s3_bucket.guide_pdfs](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/s3_bucket) | data source |
| [aws_subnet_ids.private](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/subnet_ids) | data source |
| [aws_subnet_ids.public](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/subnet_ids) | data source |
| [aws_vpc.this](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/vpc) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_app_access_cidr"></a> [app\_access\_cidr](#input\_app\_access\_cidr) | This CIDR will be allowed to connect to Dozuki. If running a public site, use the default value. Otherwise you probably want to lock this down to the VPC or your VPN CIDR. | `string` | `"0.0.0.0/0"` | no |
| <a name="input_aws_profile"></a> [aws\_profile](#input\_aws\_profile) | If running terraform from a workstation, which AWS CLI profile should we use for asset provisioning. | `string` | `"default"` | no |
| <a name="input_azs_count"></a> [azs\_count](#input\_azs\_count) | The number of availability zones we should use for deployment. | `number` | `3` | no |
| <a name="input_create_s3_buckets"></a> [create\_s3\_buckets](#input\_create\_s3\_buckets) | Wheter to create the dozuki S3 buckets or not. | `bool` | `true` | no |
| <a name="input_eks_desired_capacity"></a> [eks\_desired\_capacity](#input\_eks\_desired\_capacity) | This is what the node count will start out as. | `number` | `"3"` | no |
| <a name="input_eks_instance_types"></a> [eks\_instance\_types](#input\_eks\_instance\_types) | The instance type of each node in the application's EKS worker node group. | `list(string)` | <pre>[<br>  "m5.large",<br>  "m5a.large",<br>  "m5d.large",<br>  "m5ad.large"<br>]</pre> | no |
| <a name="input_eks_kms_key_id"></a> [eks\_kms\_key\_id](#input\_eks\_kms\_key\_id) | AWS KMS key identifier for EKS encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN | `string` | `""` | no |
| <a name="input_eks_max_size"></a> [eks\_max\_size](#input\_eks\_max\_size) | The maximum amount of nodes we will autoscale to. | `number` | `"10"` | no |
| <a name="input_eks_min_size"></a> [eks\_min\_size](#input\_eks\_min\_size) | The minimum amount of nodes we will autoscale to. | `number` | `"2"` | no |
| <a name="input_eks_volume_size"></a> [eks\_volume\_size](#input\_eks\_volume\_size) | The amount of local storage (in gigabytes) to allocate to each kubernetes node. Keep in mind you will be billed for this amount of storage multiplied by how many nodes you spin up (i.e. 50GB * 4 nodes = 200GB on your bill). For production installations 50GB should be the minimum. This local storage is used as a temporary holding area for uploaded and in-process assets like videos and images. | `number` | `50` | no |
| <a name="input_elasticache_cluster_size"></a> [elasticache\_cluster\_size](#input\_elasticache\_cluster\_size) | Cluster size | `number` | `1` | no |
| <a name="input_elasticache_instance_type"></a> [elasticache\_instance\_type](#input\_elasticache\_instance\_type) | Elastic cache instance type | `string` | `"cache.t2.micro"` | no |
| <a name="input_enable_bi"></a> [enable\_bi](#input\_enable\_bi) | This option will spin up a BI slave of your master database and enable conditional replication (everything but the mysql table will be replicated so you can have custom users). | `bool` | `false` | no |
| <a name="input_enable_webhooks"></a> [enable\_webhooks](#input\_enable\_webhooks) | This option will spin up a managed Kafka & Redis cluster to support private webhooks. | `bool` | `false` | no |
| <a name="input_environment"></a> [environment](#input\_environment) | Environment of the application | `string` | `"dev"` | no |
| <a name="input_highly_available_nat_gateway"></a> [highly\_available\_nat\_gateway](#input\_highly\_available\_nat\_gateway) | Should be true if you want to provision a highly available NAT Gateway across all of your private networks | `bool` | `true` | no |
| <a name="input_identifier"></a> [identifier](#input\_identifier) | A name identifier to use as prefix for all the resources. | `string` | `""` | no |
| <a name="input_protect_resources"></a> [protect\_resources](#input\_protect\_resources) | Specifies whether data protection settings are enabled. If true they will prevent stack deletion until protections have been manually disabled. | `bool` | `true` | no |
| <a name="input_public_access"></a> [public\_access](#input\_public\_access) | Should the app and dashboard be accessible via a publicly routable IP and domain? | `bool` | `true` | no |
| <a name="input_rds_allocated_storage"></a> [rds\_allocated\_storage](#input\_rds\_allocated\_storage) | The initial size of the database (Gb) | `number` | `100` | no |
| <a name="input_rds_backup_retention_period"></a> [rds\_backup\_retention\_period](#input\_rds\_backup\_retention\_period) | The number of days to keep automatic database backups. Setting this value to 0 disables automatic backups. | `number` | `30` | no |
| <a name="input_rds_instance_type"></a> [rds\_instance\_type](#input\_rds\_instance\_type) | The instance type to use for your database. See this page for a breakdown of the performance and cost differences between the different instance types: https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.DBInstanceClass.html | `string` | `"db.m4.large"` | no |
| <a name="input_rds_kms_key_id"></a> [rds\_kms\_key\_id](#input\_rds\_kms\_key\_id) | AWS KMS key identifier for RDS encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN | `string` | `"alias/aws/rds"` | no |
| <a name="input_rds_max_allocated_storage"></a> [rds\_max\_allocated\_storage](#input\_rds\_max\_allocated\_storage) | The maximum size to which AWS will scale the database (Gb) | `number` | `500` | no |
| <a name="input_rds_multi_az"></a> [rds\_multi\_az](#input\_rds\_multi\_az) | If true we will tell RDS to automatically deploy and manage a highly available standby instance of your database. Enabling this doubles the cost of the RDS instance but without it you are susceptible to downtime if the AWS availability zone your RDS instance is in becomes unavailable. | `bool` | `true` | no |
| <a name="input_rds_snapshot_identifier"></a> [rds\_snapshot\_identifier](#input\_rds\_snapshot\_identifier) | We can seed the database from an existing RDS snapshot in this region. Type the snapshot identifier in this field or leave blank to start with a fresh database. Note: If you do use a snapshot it's critical that during stack updates you continue to include the snapshot identifier in this parameter. Clearing this parameter after using it will cause AWS to spin up a new fresh DB and delete your old one. | `string` | `""` | no |
| <a name="input_replicated_ui_access_cidr"></a> [replicated\_ui\_access\_cidr](#input\_replicated\_ui\_access\_cidr) | This CIDR will be allowed to connect to the app dashboard. This is where you upgrade to new versions as well as view cluster status and start/stop the cluster. You probably want to lock this down to your company network CIDR, especially if you chose 'true' for public access. | `string` | `"0.0.0.0/0"` | no |
| <a name="input_s3_documents_bucket"></a> [s3\_documents\_bucket](#input\_s3\_documents\_bucket) | Name of the bucket to store documents. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_images_bucket"></a> [s3\_images\_bucket](#input\_s3\_images\_bucket) | Name of the bucket to store guide images. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_kms_key_id"></a> [s3\_kms\_key\_id](#input\_s3\_kms\_key\_id) | AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN | `string` | `"alias/aws/s3"` | no |
| <a name="input_s3_objects_bucket"></a> [s3\_objects\_bucket](#input\_s3\_objects\_bucket) | Name of the bucket to store guide objects. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_pdfs_bucket"></a> [s3\_pdfs\_bucket](#input\_s3\_pdfs\_bucket) | Name of the bucket to store guide pdfs. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_vpc_cidr"></a> [vpc\_cidr](#input\_vpc\_cidr) | The CIDR block for the VPC | `string` | `"172.16.0.0/16"` | no |
| <a name="input_vpc_id"></a> [vpc\_id](#input\_vpc\_id) | The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private | `string` | `""` | no |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_azs_count"></a> [azs\_count](#output\_azs\_count) | n/a |
| <a name="output_cluster_primary_sg"></a> [cluster\_primary\_sg](#output\_cluster\_primary\_sg) | Primary security group for EKS cluster |
| <a name="output_dms_task_arn"></a> [dms\_task\_arn](#output\_dms\_task\_arn) | DMS Replication Task ARN for BI |
| <a name="output_documents_bucket"></a> [documents\_bucket](#output\_documents\_bucket) | n/a |
| <a name="output_eks_cluster_access_role_arn"></a> [eks\_cluster\_access\_role\_arn](#output\_eks\_cluster\_access\_role\_arn) | IAM Role ARN for EKS cluster access |
| <a name="output_eks_cluster_id"></a> [eks\_cluster\_id](#output\_eks\_cluster\_id) | EKS Cluster Name |
| <a name="output_eks_oidc_cluster_access_role_name"></a> [eks\_oidc\_cluster\_access\_role\_name](#output\_eks\_oidc\_cluster\_access\_role\_name) | OIDC-compatible IAM role name for EKS cluster access |
| <a name="output_eks_worker_asg_arns"></a> [eks\_worker\_asg\_arns](#output\_eks\_worker\_asg\_arns) | EKS worker autoscaling group ARNs |
| <a name="output_eks_worker_asg_names"></a> [eks\_worker\_asg\_names](#output\_eks\_worker\_asg\_names) | EKS worker autoscaling group names |
| <a name="output_guide_images_bucket"></a> [guide\_images\_bucket](#output\_guide\_images\_bucket) | n/a |
| <a name="output_guide_objects_bucket"></a> [guide\_objects\_bucket](#output\_guide\_objects\_bucket) | n/a |
| <a name="output_guide_pdfs_bucket"></a> [guide\_pdfs\_bucket](#output\_guide\_pdfs\_bucket) | n/a |
| <a name="output_memcached_cluster_address"></a> [memcached\_cluster\_address](#output\_memcached\_cluster\_address) | n/a |
| <a name="output_msk_bootstrap_brokers"></a> [msk\_bootstrap\_brokers](#output\_msk\_bootstrap\_brokers) | Kafka bootstrap broker list |
| <a name="output_nlb_dns_name"></a> [nlb\_dns\_name](#output\_nlb\_dns\_name) | URL to deployed application |
| <a name="output_primary_db_secret"></a> [primary\_db\_secret](#output\_primary\_db\_secret) | Secretmanager ARN to MySQL credential storage |
| <a name="output_termination_handler_role_arn"></a> [termination\_handler\_role\_arn](#output\_termination\_handler\_role\_arn) | IAM Role arn for EKS node termination handler |
| <a name="output_termination_handler_sqs_queue_id"></a> [termination\_handler\_sqs\_queue\_id](#output\_termination\_handler\_sqs\_queue\_id) | SQS Queue ID for EKS node termination handler |
| <a name="output_vpc_id"></a> [vpc\_id](#output\_vpc\_id) | VPC ID |
<!-- END_TF_DOCS -->