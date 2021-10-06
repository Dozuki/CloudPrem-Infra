<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | 3.56.0 |
| <a name="requirement_random"></a> [random](#requirement\_random) | 3.1.0 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | 3.56.0 |
| <a name="provider_null"></a> [null](#provider\_null) | n/a |
| <a name="provider_random"></a> [random](#provider\_random) | 3.1.0 |

## Modules

| Name | Source | Version |
|------|--------|---------|
| <a name="module_bastion"></a> [bastion](#module\_bastion) | terraform-aws-modules/autoscaling/aws | 3.8.0 |
| <a name="module_bastion_role"></a> [bastion\_role](#module\_bastion\_role) | terraform-aws-modules/iam/aws//modules/iam-assumable-role | 4.3.0 |
| <a name="module_bastion_sg"></a> [bastion\_sg](#module\_bastion\_sg) | terraform-aws-modules/security-group/aws | 4.3.0 |
| <a name="module_database_sg"></a> [database\_sg](#module\_database\_sg) | terraform-aws-modules/security-group/aws | 4.3.0 |
| <a name="module_documents_s3_bucket"></a> [documents\_s3\_bucket](#module\_documents\_s3\_bucket) | terraform-aws-modules/s3-bucket/aws | 2.9.0 |
| <a name="module_guide_images_s3_bucket"></a> [guide\_images\_s3\_bucket](#module\_guide\_images\_s3\_bucket) | terraform-aws-modules/s3-bucket/aws | 2.9.0 |
| <a name="module_guide_objects_s3_bucket"></a> [guide\_objects\_s3\_bucket](#module\_guide\_objects\_s3\_bucket) | terraform-aws-modules/s3-bucket/aws | 2.9.0 |
| <a name="module_guide_pdfs_s3_bucket"></a> [guide\_pdfs\_s3\_bucket](#module\_guide\_pdfs\_s3\_bucket) | terraform-aws-modules/s3-bucket/aws | 2.9.0 |
| <a name="module_primary_database"></a> [primary\_database](#module\_primary\_database) | terraform-aws-modules/rds/aws | 3.3.0 |
| <a name="module_replica_database"></a> [replica\_database](#module\_replica\_database) | terraform-aws-modules/rds/aws | 3.3.0 |

## Resources

| Name | Type |
|------|------|
| [aws_dms_endpoint.source](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/dms_endpoint) | resource |
| [aws_dms_endpoint.target](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/dms_endpoint) | resource |
| [aws_dms_replication_instance.this](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/dms_replication_instance) | resource |
| [aws_dms_replication_subnet_group.this](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/dms_replication_subnet_group) | resource |
| [aws_dms_replication_task.this](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/dms_replication_task) | resource |
| [aws_elasticache_cluster.this](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/elasticache_cluster) | resource |
| [aws_elasticache_parameter_group.this](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/elasticache_parameter_group) | resource |
| [aws_elasticache_subnet_group.this](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/elasticache_subnet_group) | resource |
| [aws_secretsmanager_secret.primary_database_credentials](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/secretsmanager_secret) | resource |
| [aws_secretsmanager_secret.replica_database](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/secretsmanager_secret) | resource |
| [aws_secretsmanager_secret_version.primary_database_credentials](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/secretsmanager_secret_version) | resource |
| [aws_secretsmanager_secret_version.replica_database](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/secretsmanager_secret_version) | resource |
| [aws_security_group.elasticache](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/security_group) | resource |
| [aws_security_group_rule.egress](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.ingress_cidr_blocks](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/security_group_rule) | resource |
| [null_resource.cluster_urls](https://registry.terraform.io/providers/hashicorp/null/latest/docs/resources/resource) | resource |
| [null_resource.replication_control](https://registry.terraform.io/providers/hashicorp/null/latest/docs/resources/resource) | resource |
| [random_password.primary_database](https://registry.terraform.io/providers/hashicorp/random/3.1.0/docs/resources/password) | resource |
| [random_password.replica_database](https://registry.terraform.io/providers/hashicorp/random/3.1.0/docs/resources/password) | resource |
| [aws_ami.amazon_linux_2](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/ami) | data source |
| [aws_kms_key.rds](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/kms_key) | data source |
| [aws_partition.current](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/partition) | data source |
| [aws_region.current](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/region) | data source |
| [aws_s3_bucket.documents](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/s3_bucket) | data source |
| [aws_s3_bucket.guide_images](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/s3_bucket) | data source |
| [aws_s3_bucket.guide_objects](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/s3_bucket) | data source |
| [aws_s3_bucket.guide_pdfs](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/s3_bucket) | data source |
| [aws_subnets.private](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/subnets) | data source |
| [aws_vpc.main](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/vpc) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_create_s3_buckets"></a> [create\_s3\_buckets](#input\_create\_s3\_buckets) | Wheter to create the dozuki S3 buckets or not. | `bool` | `true` | no |
| <a name="input_eks_cluster_access_role_arn"></a> [eks\_cluster\_access\_role\_arn](#input\_eks\_cluster\_access\_role\_arn) | ARN for cluster access role for app provisioning | `string` | n/a | yes |
| <a name="input_eks_cluster_id"></a> [eks\_cluster\_id](#input\_eks\_cluster\_id) | ID of EKS cluster for app provisioning | `string` | n/a | yes |
| <a name="input_elasticache_cluster_size"></a> [elasticache\_cluster\_size](#input\_elasticache\_cluster\_size) | Cluster size | `number` | `1` | no |
| <a name="input_elasticache_instance_type"></a> [elasticache\_instance\_type](#input\_elasticache\_instance\_type) | Elastic cache instance type | `string` | `"cache.t2.micro"` | no |
| <a name="input_enable_bi"></a> [enable\_bi](#input\_enable\_bi) | This option will spin up a BI slave of your master database and enable conditional replication (everything but the mysql table will be replicated so you can have custom users). | `bool` | `false` | no |
| <a name="input_environment"></a> [environment](#input\_environment) | Environment of the application | `string` | `"dev"` | no |
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
| <a name="input_s3_documents_bucket"></a> [s3\_documents\_bucket](#input\_s3\_documents\_bucket) | Name of the bucket to store documents. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_images_bucket"></a> [s3\_images\_bucket](#input\_s3\_images\_bucket) | Name of the bucket to store guide images. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_kms_key_id"></a> [s3\_kms\_key\_id](#input\_s3\_kms\_key\_id) | AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN | `string` | `"alias/aws/s3"` | no |
| <a name="input_s3_objects_bucket"></a> [s3\_objects\_bucket](#input\_s3\_objects\_bucket) | Name of the bucket to store guide objects. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_pdfs_bucket"></a> [s3\_pdfs\_bucket](#input\_s3\_pdfs\_bucket) | Name of the bucket to store guide pdfs. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_vpc_id"></a> [vpc\_id](#input\_vpc\_id) | The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private | `string` | n/a | yes |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_documents_bucket"></a> [documents\_bucket](#output\_documents\_bucket) | n/a |
| <a name="output_guide_images_bucket"></a> [guide\_images\_bucket](#output\_guide\_images\_bucket) | n/a |
| <a name="output_guide_objects_bucket"></a> [guide\_objects\_bucket](#output\_guide\_objects\_bucket) | n/a |
| <a name="output_guide_pdfs_bucket"></a> [guide\_pdfs\_bucket](#output\_guide\_pdfs\_bucket) | n/a |
| <a name="output_memcached_cluster_address"></a> [memcached\_cluster\_address](#output\_memcached\_cluster\_address) | n/a |
| <a name="output_primary_db_secret"></a> [primary\_db\_secret](#output\_primary\_db\_secret) | n/a |
<!-- END_TF_DOCS -->