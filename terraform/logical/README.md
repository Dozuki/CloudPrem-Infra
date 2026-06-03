<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.5.0 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | ~> 6.0 |
| <a name="requirement_helm"></a> [helm](#requirement\_helm) | ~> 2.0 |
| <a name="requirement_kubernetes"></a> [kubernetes](#requirement\_kubernetes) | ~> 2.0 |
| <a name="requirement_local"></a> [local](#requirement\_local) | ~> 2.0 |
| <a name="requirement_null"></a> [null](#requirement\_null) | ~> 3.0 |
| <a name="requirement_random"></a> [random](#requirement\_random) | ~> 3.0 |
| <a name="requirement_vault"></a> [vault](#requirement\_vault) | ~> 4.0 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | ~> 6.0 |
| <a name="provider_helm"></a> [helm](#provider\_helm) | ~> 2.0 |
| <a name="provider_kubernetes"></a> [kubernetes](#provider\_kubernetes) | ~> 2.0 |
| <a name="provider_random"></a> [random](#provider\_random) | ~> 3.0 |
| <a name="provider_vault"></a> [vault](#provider\_vault) | ~> 4.0 |

## Modules

No modules.

## Resources

| Name | Type |
|------|------|
| [aws_eks_addon.cloudwatch_observability](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/eks_addon) | resource |
| [helm_release.app](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.cert_manager](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.envoy_gateway](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.external_secrets](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [kubernetes_cluster_role_binding_v1.dozuki_list_role_binding](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/cluster_role_binding_v1) | resource |
| [kubernetes_cluster_role_binding_v1.vault_auth_delegator](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/cluster_role_binding_v1) | resource |
| [kubernetes_cluster_role_v1.dozuki_list_role](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/cluster_role_v1) | resource |
| [kubernetes_config_map_v1.frontegg_db_script](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/config_map_v1) | resource |
| [kubernetes_config_map_v1.grafana_create_db_script](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/config_map_v1) | resource |
| [kubernetes_job_v1.dms_start](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/job_v1) | resource |
| [kubernetes_job_v1.frontegg_db_create](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/job_v1) | resource |
| [kubernetes_job_v1.grafana_db_create](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/job_v1) | resource |
| [kubernetes_manifest.nodepool_on_demand](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/manifest) | resource |
| [kubernetes_manifest.nodepool_spot](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/manifest) | resource |
| [kubernetes_manifest.tgb_http](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/manifest) | resource |
| [kubernetes_manifest.tgb_https](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/manifest) | resource |
| [kubernetes_namespace_v1.app](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/namespace_v1) | resource |
| [kubernetes_namespace_v1.cert_manager](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/namespace_v1) | resource |
| [kubernetes_role_binding_v1.dozuki_subsite_role_binding](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/role_binding_v1) | resource |
| [kubernetes_role_v1.dozuki_subsite_role](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/role_v1) | resource |
| [kubernetes_secret_v1.frontegg_db_credentials](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.grafana_db_credentials](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.vault_auth_token](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_service_account_v1.eso_vault_auth](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/service_account_v1) | resource |
| [kubernetes_service_account_v1.vault_auth](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/service_account_v1) | resource |
| [kubernetes_service_v1.envoy_proxy](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/service_v1) | resource |
| [kubernetes_storage_class_v1.ebs_gp3](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/storage_class_v1) | resource |
| [random_password.grafana_admin](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [vault_auth_backend.kubernetes](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/auth_backend) | resource |
| [vault_aws_auth_backend_role.stack](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/aws_auth_backend_role) | resource |
| [vault_kubernetes_auth_backend_config.stack](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kubernetes_auth_backend_config) | resource |
| [vault_kubernetes_auth_backend_role.eso](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kubernetes_auth_backend_role) | resource |
| [vault_kv_secret_v2.bi](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.cache](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.db](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.google_translate](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.smtp](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_policy.eso_readonly](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/policy) | resource |
| [vault_policy.stack](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/policy) | resource |
| [aws_caller_identity.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/caller_identity) | data source |
| [aws_eks_cluster.main](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/eks_cluster) | data source |
| [aws_kms_key.s3](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/kms_key) | data source |
| [aws_partition.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/partition) | data source |
| [aws_region.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/region) | data source |
| [aws_secretsmanager_secret_version.db_bi](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/secretsmanager_secret_version) | data source |
| [aws_secretsmanager_secret_version.db_master](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/secretsmanager_secret_version) | data source |
| [vault_kv_secret_v2.global_frontegg](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/data-sources/kv_secret_v2) | data source |
| [vault_kv_secret_v2.global_ops](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/data-sources/kv_secret_v2) | data source |
| [vault_kv_secret_v2.global_rustici](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/data-sources/kv_secret_v2) | data source |
| [vault_kv_secret_v2.global_sentry](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/data-sources/kv_secret_v2) | data source |
| [vault_kv_secret_v2.global_slack](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/data-sources/kv_secret_v2) | data source |
| [vault_kv_secret_v2.global_surveyjs](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/data-sources/kv_secret_v2) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_aws_profile"></a> [aws\_profile](#input\_aws\_profile) | If running terraform from a workstation, which AWS CLI profile should we use for asset provisioning. | `string` | `""` | no |
| <a name="input_bi_database_credential_secret"></a> [bi\_database\_credential\_secret](#input\_bi\_database\_credential\_secret) | ARN to secret containing bi db credentials | `string` | `""` | no |
| <a name="input_customer"></a> [customer](#input\_customer) | The customer name for resource names and tagging. This will also be the autogenerated subdomain. | `string` | `""` | no |
| <a name="input_dms_enabled"></a> [dms\_enabled](#input\_dms\_enabled) | If BI is enabled, whether or not to use DMS for conditional replication if true or a basic RDS read replica if false. | `bool` | `false` | no |
| <a name="input_dms_task_arn"></a> [dms\_task\_arn](#input\_dms\_task\_arn) | If BI is enabled, the DMS replication task arn. | `string` | n/a | yes |
| <a name="input_dns_domain_name"></a> [dns\_domain\_name](#input\_dns\_domain\_name) | Auto-provisioned subdomain for this environment | `string` | n/a | yes |
| <a name="input_eks_cluster_id"></a> [eks\_cluster\_id](#input\_eks\_cluster\_id) | ID of EKS cluster for app provisioning | `string` | n/a | yes |
| <a name="input_enable_bi"></a> [enable\_bi](#input\_enable\_bi) | Whether to deploy resources for BI, a replica database, a DMS task, and a Kafka cluster | `bool` | `false` | no |
| <a name="input_enable_webhooks"></a> [enable\_webhooks](#input\_enable\_webhooks) | This option will spin up a managed Kafka & Redis cluster to support private webhooks. | `bool` | `false` | no |
| <a name="input_environment"></a> [environment](#input\_environment) | Environment of the application | `string` | `"dev"` | no |
| <a name="input_google_translate_api_token"></a> [google\_translate\_api\_token](#input\_google\_translate\_api\_token) | If using machine translation, enter your google translate API token here. | `string` | `""` | no |
| <a name="input_grafana_subpath"></a> [grafana\_subpath](#input\_grafana\_subpath) | Subpath to serve Grafana from | `string` | `"dashboards"` | no |
| <a name="input_image_repository"></a> [image\_repository](#input\_image\_repository) | Docker image repository (ECR) for app containers. | `string` | n/a | yes |
| <a name="input_image_tag"></a> [image\_tag](#input\_image\_tag) | Docker image tag for the main Dozuki app container. Changes with every deploy. | `string` | n/a | yes |
| <a name="input_ingress_hostname"></a> [ingress\_hostname](#input\_ingress\_hostname) | Hostname for the app ingress. Set to a wildcard (e.g. *.customer.com) for customer-provided certs. Defaults to dns\_domain\_name. | `string` | `""` | no |
| <a name="input_memcached_cluster_address"></a> [memcached\_cluster\_address](#input\_memcached\_cluster\_address) | Address of the deployed memcached cluster | `string` | n/a | yes |
| <a name="input_msk_bootstrap_brokers"></a> [msk\_bootstrap\_brokers](#input\_msk\_bootstrap\_brokers) | Kafka bootstrap broker list | `any` | n/a | yes |
| <a name="input_nextjs_tag"></a> [nextjs\_tag](#input\_nextjs\_tag) | Docker image tag for the Next.js frontend container. Changes with every deploy. | `string` | n/a | yes |
| <a name="input_nlb_http_target_group_arn"></a> [nlb\_http\_target\_group\_arn](#input\_nlb\_http\_target\_group\_arn) | NLB HTTP target group ARN for TargetGroupBinding | `string` | n/a | yes |
| <a name="input_nlb_https_target_group_arn"></a> [nlb\_https\_target\_group\_arn](#input\_nlb\_https\_target\_group\_arn) | NLB HTTPS target group ARN for TargetGroupBinding | `string` | n/a | yes |
| <a name="input_primary_db_secret"></a> [primary\_db\_secret](#input\_primary\_db\_secret) | ARN to secret containing primary db credentials | `string` | n/a | yes |
| <a name="input_protect_resources"></a> [protect\_resources](#input\_protect\_resources) | When true, retain Vault secrets on destroy (soft delete). When false, permanently purge all versions. | `bool` | `true` | no |
| <a name="input_s3_documents_bucket"></a> [s3\_documents\_bucket](#input\_s3\_documents\_bucket) | Name of the bucket to store documents. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_images_bucket"></a> [s3\_images\_bucket](#input\_s3\_images\_bucket) | Name of the bucket to store guide images. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_kms_key_id"></a> [s3\_kms\_key\_id](#input\_s3\_kms\_key\_id) | AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN | `string` | `""` | no |
| <a name="input_s3_objects_bucket"></a> [s3\_objects\_bucket](#input\_s3\_objects\_bucket) | Name of the bucket to store guide objects. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_pdfs_bucket"></a> [s3\_pdfs\_bucket](#input\_s3\_pdfs\_bucket) | Name of the bucket to store guide pdfs. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_replicate_buckets"></a> [s3\_replicate\_buckets](#input\_s3\_replicate\_buckets) | Whether or not we are replicating objects from existing S3 buckets. | `bool` | `false` | no |
| <a name="input_smtp_auth_enabled"></a> [smtp\_auth\_enabled](#input\_smtp\_auth\_enabled) | Whether to use SMTP authentication. | `bool` | `true` | no |
| <a name="input_smtp_enabled"></a> [smtp\_enabled](#input\_smtp\_enabled) | Whether to enable SMTP email sending. | `bool` | `true` | no |
| <a name="input_smtp_from_address"></a> [smtp\_from\_address](#input\_smtp\_from\_address) | SMTP from email address. | `string` | `"noreply@dozuki.com"` | no |
| <a name="input_smtp_host"></a> [smtp\_host](#input\_smtp\_host) | SMTP server hostname. | `string` | `"smtp.sendgrid.net"` | no |
| <a name="input_smtp_password"></a> [smtp\_password](#input\_smtp\_password) | SMTP authentication password. | `string` | `""` | no |
| <a name="input_smtp_username"></a> [smtp\_username](#input\_smtp\_username) | SMTP authentication username. | `string` | `"apikey"` | no |
| <a name="input_spacelift"></a> [spacelift](#input\_spacelift) | Set to true when running in Spacelift. Enables IAM auth for the Vault provider. | `bool` | `false` | no |
| <a name="input_vault_address"></a> [vault\_address](#input\_vault\_address) | Vault server address accessible from within the cluster (PrivateLink). | `string` | n/a | yes |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_dozuki_url"></a> [dozuki\_url](#output\_dozuki\_url) | URL to your Dozuki Installation. |
| <a name="output_grafana_admin_password"></a> [grafana\_admin\_password](#output\_grafana\_admin\_password) | Password for Grafana admin user |
| <a name="output_grafana_admin_username"></a> [grafana\_admin\_username](#output\_grafana\_admin\_username) | n/a |
| <a name="output_grafana_url"></a> [grafana\_url](#output\_grafana\_url) | n/a |
| <a name="output_replicate_instructions"></a> [replicate\_instructions](#output\_replicate\_instructions) | n/a |
<!-- END_TF_DOCS -->