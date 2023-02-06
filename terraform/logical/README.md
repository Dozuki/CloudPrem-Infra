<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.1.0 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | 3.70.0 |
| <a name="requirement_helm"></a> [helm](#requirement\_helm) | 2.3.0 |
| <a name="requirement_kubernetes"></a> [kubernetes](#requirement\_kubernetes) | 2.13.1 |
| <a name="requirement_local"></a> [local](#requirement\_local) | 2.2.3 |
| <a name="requirement_null"></a> [null](#requirement\_null) | 3.1.0 |
| <a name="requirement_random"></a> [random](#requirement\_random) | 3.4.3 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | 3.70.0 |
| <a name="provider_helm"></a> [helm](#provider\_helm) | 2.3.0 |
| <a name="provider_kubernetes"></a> [kubernetes](#provider\_kubernetes) | 2.13.1 |
| <a name="provider_local"></a> [local](#provider\_local) | 2.2.3 |
| <a name="provider_null"></a> [null](#provider\_null) | 3.1.0 |
| <a name="provider_random"></a> [random](#provider\_random) | 3.4.3 |

## Modules

| Name | Source | Version |
|------|--------|---------|
| <a name="module_grafana_ssl_cert"></a> [grafana\_ssl\_cert](#module\_grafana\_ssl\_cert) | ../common/acm | n/a |
| <a name="module_ssl_cert"></a> [ssl\_cert](#module\_ssl\_cert) | ../common/acm | n/a |

## Resources

| Name | Type |
|------|------|
| [aws_autoscaling_lifecycle_hook.aws_node_termination_handler](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/resources/autoscaling_lifecycle_hook) | resource |
| [helm_release.aws_node_termination_handler](https://registry.terraform.io/providers/hashicorp/helm/2.3.0/docs/resources/release) | resource |
| [helm_release.cluster_autoscaler](https://registry.terraform.io/providers/hashicorp/helm/2.3.0/docs/resources/release) | resource |
| [helm_release.container_insights](https://registry.terraform.io/providers/hashicorp/helm/2.3.0/docs/resources/release) | resource |
| [helm_release.frontegg](https://registry.terraform.io/providers/hashicorp/helm/2.3.0/docs/resources/release) | resource |
| [helm_release.grafana](https://registry.terraform.io/providers/hashicorp/helm/2.3.0/docs/resources/release) | resource |
| [helm_release.metrics_server](https://registry.terraform.io/providers/hashicorp/helm/2.3.0/docs/resources/release) | resource |
| [helm_release.mongodb](https://registry.terraform.io/providers/hashicorp/helm/2.3.0/docs/resources/release) | resource |
| [helm_release.redis](https://registry.terraform.io/providers/hashicorp/helm/2.3.0/docs/resources/release) | resource |
| [kubernetes_annotations.www_tls](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/annotations) | resource |
| [kubernetes_config_map.dozuki_resources](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/config_map) | resource |
| [kubernetes_horizontal_pod_autoscaler.app](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/horizontal_pod_autoscaler) | resource |
| [kubernetes_horizontal_pod_autoscaler.queueworkerd](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/horizontal_pod_autoscaler) | resource |
| [kubernetes_job.dms_start](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/job) | resource |
| [kubernetes_job.frontegg_database_create](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/job) | resource |
| [kubernetes_job.sites_config_update](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/job) | resource |
| [kubernetes_job.wait_for_app](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/job) | resource |
| [kubernetes_namespace.kots_app](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/namespace) | resource |
| [kubernetes_secret.grafana_config](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/secret) | resource |
| [kubernetes_secret.grafana_ssl](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/secret) | resource |
| [kubernetes_secret.site_tls](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/resources/secret) | resource |
| [local_file.replicated_install](https://registry.terraform.io/providers/hashicorp/local/2.2.3/docs/resources/file) | resource |
| [null_resource.pull_replicated_license](https://registry.terraform.io/providers/hashicorp/null/3.1.0/docs/resources/resource) | resource |
| [random_password.dashboard_password](https://registry.terraform.io/providers/hashicorp/random/3.4.3/docs/resources/password) | resource |
| [random_password.grafana_admin](https://registry.terraform.io/providers/hashicorp/random/3.4.3/docs/resources/password) | resource |
| [aws_caller_identity.current](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/caller_identity) | data source |
| [aws_eks_cluster.main](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/eks_cluster) | data source |
| [aws_kms_key.s3](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/kms_key) | data source |
| [aws_partition.current](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/partition) | data source |
| [aws_region.current](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/region) | data source |
| [aws_secretsmanager_secret_version.db_bi](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/secretsmanager_secret_version) | data source |
| [aws_secretsmanager_secret_version.db_master](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/secretsmanager_secret_version) | data source |
| [aws_ssm_parameter.dozuki_customer_id](https://registry.terraform.io/providers/hashicorp/aws/3.70.0/docs/data-sources/ssm_parameter) | data source |
| [kubernetes_secret.frontegg](https://registry.terraform.io/providers/hashicorp/kubernetes/2.13.1/docs/data-sources/secret) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_aws_profile"></a> [aws\_profile](#input\_aws\_profile) | If running terraform from a workstation, which AWS CLI profile should we use for asset provisioning. | `string` | `""` | no |
| <a name="input_azs_count"></a> [azs\_count](#input\_azs\_count) | The number of availability zones we should use for deployment. | `number` | `3` | no |
| <a name="input_bi_database_credential_secret"></a> [bi\_database\_credential\_secret](#input\_bi\_database\_credential\_secret) | ARN to secret containing bi db credentials | `string` | `""` | no |
| <a name="input_dms_task_arn"></a> [dms\_task\_arn](#input\_dms\_task\_arn) | If BI is enabled, the DMS replication task arn. | `string` | n/a | yes |
| <a name="input_dozuki_customer_id_parameter_name"></a> [dozuki\_customer\_id\_parameter\_name](#input\_dozuki\_customer\_id\_parameter\_name) | Parameter name for dozuki customer id in AWS Parameter store. | `string` | `""` | no |
| <a name="input_eks_cluster_access_role_arn"></a> [eks\_cluster\_access\_role\_arn](#input\_eks\_cluster\_access\_role\_arn) | ARN for the IAM Role for API-based EKS cluster access. | `string` | n/a | yes |
| <a name="input_eks_cluster_id"></a> [eks\_cluster\_id](#input\_eks\_cluster\_id) | ID of EKS cluster for app provisioning | `string` | n/a | yes |
| <a name="input_eks_oidc_cluster_access_role_name"></a> [eks\_oidc\_cluster\_access\_role\_name](#input\_eks\_oidc\_cluster\_access\_role\_name) | ARN for OIDC-compatible IAM Role for the EKS Cluster Autoscaler | `string` | n/a | yes |
| <a name="input_eks_worker_asg_names"></a> [eks\_worker\_asg\_names](#input\_eks\_worker\_asg\_names) | Autoscaling group names for the EKS cluster | `list(string)` | n/a | yes |
| <a name="input_enable_bi"></a> [enable\_bi](#input\_enable\_bi) | Whether to deploy resources for BI, a replica database, a DMS task, and a Kafka cluster | `string` | `false` | no |
| <a name="input_enable_webhooks"></a> [enable\_webhooks](#input\_enable\_webhooks) | This option will spin up a managed Kafka & Redis cluster to support private webhooks. | `bool` | `false` | no |
| <a name="input_environment"></a> [environment](#input\_environment) | Environment of the application | `string` | `"dev"` | no |
| <a name="input_google_translate_api_token"></a> [google\_translate\_api\_token](#input\_google\_translate\_api\_token) | If using machine translation, enter your google translate API token here. | `string` | `""` | no |
| <a name="input_grafana_ssl_cn"></a> [grafana\_ssl\_cn](#input\_grafana\_ssl\_cn) | If a custom common name is required for the self-signed SSL certificate for Grafana (if BI is enabled), set it here. | `string` | `""` | no |
| <a name="input_grafana_use_replicated_ssl"></a> [grafana\_use\_replicated\_ssl](#input\_grafana\_use\_replicated\_ssl) | If true the Grafana installation will use the same SSL cert uploaded (or generated by) to the Replicated dashboard. | `bool` | `true` | no |
| <a name="input_identifier"></a> [identifier](#input\_identifier) | A name identifier to use as prefix for all the resources. | `string` | `""` | no |
| <a name="input_memcached_cluster_address"></a> [memcached\_cluster\_address](#input\_memcached\_cluster\_address) | Address of the deployed memcached cluster | `string` | n/a | yes |
| <a name="input_msk_bootstrap_brokers"></a> [msk\_bootstrap\_brokers](#input\_msk\_bootstrap\_brokers) | Kafka bootstrap broker list | `any` | n/a | yes |
| <a name="input_nlb_dns_name"></a> [nlb\_dns\_name](#input\_nlb\_dns\_name) | DNS address of the network load balancer and URL to the deployed application | `string` | n/a | yes |
| <a name="input_primary_db_secret"></a> [primary\_db\_secret](#input\_primary\_db\_secret) | ARN to secret containing primary db credentials | `string` | n/a | yes |
| <a name="input_replicated_channel"></a> [replicated\_channel](#input\_replicated\_channel) | If specifying an app sequence for a fresh install, this is the channel that sequence was deployed to. You only need to set this if the sequence you configured was not released on the default channel associated with your customer license. | `string` | `""` | no |
| <a name="input_s3_documents_bucket"></a> [s3\_documents\_bucket](#input\_s3\_documents\_bucket) | Name of the bucket to store documents. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_images_bucket"></a> [s3\_images\_bucket](#input\_s3\_images\_bucket) | Name of the bucket to store guide images. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_kms_key_id"></a> [s3\_kms\_key\_id](#input\_s3\_kms\_key\_id) | AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN | `string` | `"alias/aws/s3"` | no |
| <a name="input_s3_objects_bucket"></a> [s3\_objects\_bucket](#input\_s3\_objects\_bucket) | Name of the bucket to store guide objects. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_pdfs_bucket"></a> [s3\_pdfs\_bucket](#input\_s3\_pdfs\_bucket) | Name of the bucket to store guide pdfs. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_termination_handler_role_arn"></a> [termination\_handler\_role\_arn](#input\_termination\_handler\_role\_arn) | IAM Role for EKS node termination handler | `string` | n/a | yes |
| <a name="input_termination_handler_sqs_queue_id"></a> [termination\_handler\_sqs\_queue\_id](#input\_termination\_handler\_sqs\_queue\_id) | SQS Queue ID for the EKS node termination handler | `string` | n/a | yes |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_dashboard_password"></a> [dashboard\_password](#output\_dashboard\_password) | Password for your Dozuki Dashboard. |
| <a name="output_dashboard_url"></a> [dashboard\_url](#output\_dashboard\_url) | URL to your Dozuki Dashboard. |
| <a name="output_dozuki_url"></a> [dozuki\_url](#output\_dozuki\_url) | URL to your Dozuki Installation. |
| <a name="output_grafana_admin_password"></a> [grafana\_admin\_password](#output\_grafana\_admin\_password) | Password for Grafana admin user |
| <a name="output_grafana_admin_username"></a> [grafana\_admin\_username](#output\_grafana\_admin\_username) | n/a |
| <a name="output_grafana_url"></a> [grafana\_url](#output\_grafana\_url) | n/a |
<!-- END_TF_DOCS -->