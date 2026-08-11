<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
| ---- | ------- |
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | >= 1.11.1 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | ~> 6.0 |
| <a name="requirement_azurerm"></a> [azurerm](#requirement\_azurerm) | ~> 4.0 |
| <a name="requirement_external"></a> [external](#requirement\_external) | ~> 2.0 |
| <a name="requirement_helm"></a> [helm](#requirement\_helm) | ~> 3.0 |
| <a name="requirement_kubectl"></a> [kubectl](#requirement\_kubectl) | 2.1.5 |
| <a name="requirement_kubernetes"></a> [kubernetes](#requirement\_kubernetes) | ~> 3.0 |
| <a name="requirement_local"></a> [local](#requirement\_local) | ~> 2.0 |
| <a name="requirement_null"></a> [null](#requirement\_null) | ~> 3.0 |
| <a name="requirement_random"></a> [random](#requirement\_random) | ~> 3.0 |
| <a name="requirement_tls"></a> [tls](#requirement\_tls) | ~> 4.0 |
| <a name="requirement_vault"></a> [vault](#requirement\_vault) | ~> 5.0 |

## Providers

| Name | Version |
| ---- | ------- |
| <a name="provider_aws"></a> [aws](#provider\_aws) | ~> 6.0 |
| <a name="provider_azurerm"></a> [azurerm](#provider\_azurerm) | ~> 4.0 |
| <a name="provider_external"></a> [external](#provider\_external) | ~> 2.0 |
| <a name="provider_helm"></a> [helm](#provider\_helm) | ~> 3.0 |
| <a name="provider_kubectl"></a> [kubectl](#provider\_kubectl) | 2.1.5 |
| <a name="provider_kubernetes"></a> [kubernetes](#provider\_kubernetes) | ~> 3.0 |
| <a name="provider_random"></a> [random](#provider\_random) | ~> 3.0 |
| <a name="provider_tls"></a> [tls](#provider\_tls) | ~> 4.0 |
| <a name="provider_vault"></a> [vault](#provider\_vault) | ~> 5.0 |

## Modules

No modules.

## Resources

| Name | Type |
| ---- | ---- |
| [aws_eks_addon.cloudwatch_observability](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/eks_addon) | resource |
| [aws_eks_pod_identity_association.flux_source_controller](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/eks_pod_identity_association) | resource |
| [aws_iam_role.flux_source_controller](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role_policy.flux_source_ecr_read](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [azurerm_key_vault_secret.app](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/key_vault_secret) | resource |
| [helm_release.cert_manager](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.datadog](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.envoy_gateway](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.external_dns](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.external_secrets](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.flux](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.istio_base](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.istio_cni](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.istiod](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.ztunnel](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [kubectl_manifest.dozuki_helmrelease](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubectl_manifest.dozuki_ocirepository](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubectl_manifest.envoy_gateway_crds](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubectl_manifest.flux_relay_external_secret](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubectl_manifest.flux_relay_secret_store](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubectl_manifest.flux_slack_alert](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubectl_manifest.flux_slack_provider](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubectl_manifest.peer_auth_carveouts](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubectl_manifest.peer_auth_strict](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/resources/manifest) | resource |
| [kubernetes_cluster_role_binding_v1.dozuki_list_role_binding](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/cluster_role_binding_v1) | resource |
| [kubernetes_cluster_role_binding_v1.vault_auth_delegator](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/cluster_role_binding_v1) | resource |
| [kubernetes_cluster_role_v1.dozuki_list_role](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/cluster_role_v1) | resource |
| [kubernetes_config_map_v1.flux_values_diff](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/config_map_v1) | resource |
| [kubernetes_deployment_v1.ratelimit_redis](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/deployment_v1) | resource |
| [kubernetes_job_v1.dms_start](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/job_v1) | resource |
| [kubernetes_labels.ambient_dozuki](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/labels) | resource |
| [kubernetes_labels.ambient_envoy_gateway](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/labels) | resource |
| [kubernetes_labels.ambient_redis](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/labels) | resource |
| [kubernetes_manifest.nodepool_on_demand](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/manifest) | resource |
| [kubernetes_manifest.nodepool_spot](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/manifest) | resource |
| [kubernetes_namespace_v1.app](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/namespace_v1) | resource |
| [kubernetes_namespace_v1.cert_manager](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/namespace_v1) | resource |
| [kubernetes_namespace_v1.datadog](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/namespace_v1) | resource |
| [kubernetes_namespace_v1.flux_system](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/namespace_v1) | resource |
| [kubernetes_namespace_v1.istio_system](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/namespace_v1) | resource |
| [kubernetes_namespace_v1.ratelimit_redis](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/namespace_v1) | resource |
| [kubernetes_network_policy_v1.ratelimit_redis](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/network_policy_v1) | resource |
| [kubernetes_role_binding_v1.dozuki_subsite_role_binding](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/role_binding_v1) | resource |
| [kubernetes_role_v1.dozuki_subsite_role](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/role_v1) | resource |
| [kubernetes_secret_v1.datadog_api_key](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.flux_ghcr_pull](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.flux_slack_webhook](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.flux_values](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.gateway_tls](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.ghcr_pull](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.redis_auth](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.redis_auth_eg](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_secret_v1.vault_auth_token](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/secret_v1) | resource |
| [kubernetes_service_account_v1.eso_vault_auth](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/service_account_v1) | resource |
| [kubernetes_service_account_v1.flux_external_secrets](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/service_account_v1) | resource |
| [kubernetes_service_account_v1.vault_auth](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/service_account_v1) | resource |
| [kubernetes_service_v1.ratelimit_redis](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/service_v1) | resource |
| [kubernetes_storage_class_v1.ebs_gp3](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/resources/storage_class_v1) | resource |
| [random_password.dashboards_admin](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [random_password.dashboards_jwt](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [random_password.grafana_admin](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [random_password.ops_admin](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [random_password.redis_auth](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [random_password.seaweedfs_access_key](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [random_password.seaweedfs_filer_db](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [random_password.seaweedfs_secret_key](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/password) | resource |
| [tls_private_key.gateway](https://registry.terraform.io/providers/hashicorp/tls/latest/docs/resources/private_key) | resource |
| [tls_self_signed_cert.gateway](https://registry.terraform.io/providers/hashicorp/tls/latest/docs/resources/self_signed_cert) | resource |
| [vault_auth_backend.kubernetes](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/auth_backend) | resource |
| [vault_aws_auth_backend_role.stack](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/aws_auth_backend_role) | resource |
| [vault_kubernetes_auth_backend_config.stack](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kubernetes_auth_backend_config) | resource |
| [vault_kubernetes_auth_backend_role.eso](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kubernetes_auth_backend_role) | resource |
| [vault_kubernetes_auth_backend_role.flux_relay](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kubernetes_auth_backend_role) | resource |
| [vault_kv_secret_v2.bi](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.cache](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.db](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.google_translate](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.grafana](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_kv_secret_v2.tls](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/kv_secret_v2) | resource |
| [vault_policy.eso_readonly](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/policy) | resource |
| [vault_policy.flux_relay_readonly](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/policy) | resource |
| [vault_policy.stack](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/resources/policy) | resource |
| [aws_caller_identity.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/caller_identity) | data source |
| [aws_ecr_authorization_token.chart](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/ecr_authorization_token) | data source |
| [aws_ecr_image.app_pin](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/ecr_image) | data source |
| [aws_ecr_image.beanstalkd_pin](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/ecr_image) | data source |
| [aws_ecr_image.chart_pin](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/ecr_image) | data source |
| [aws_ecr_image.nextjs_pin](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/ecr_image) | data source |
| [aws_eks_cluster.main](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/eks_cluster) | data source |
| [aws_iam_policy_document.flux_source_assume](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_kms_key.s3](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/kms_key) | data source |
| [aws_partition.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/partition) | data source |
| [aws_region.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/region) | data source |
| [aws_secretsmanager_secret_version.db_bi](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/secretsmanager_secret_version) | data source |
| [aws_secretsmanager_secret_version.db_master](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/secretsmanager_secret_version) | data source |
| [azurerm_key_vault_secret.db_master](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/key_vault_secret) | data source |
| [azurerm_kubernetes_cluster.main](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/kubernetes_cluster) | data source |
| [external_external.ops_htpasswd_hash](https://registry.terraform.io/providers/hashicorp/external/latest/docs/data-sources/external) | data source |
| [kubectl_file_documents.envoy_gateway_crds](https://registry.terraform.io/providers/alekc/kubectl/2.1.5/docs/data-sources/file_documents) | data source |
| [kubernetes_service_v1.envoy_proxy_azure](https://registry.terraform.io/providers/hashicorp/kubernetes/latest/docs/data-sources/service_v1) | data source |
| [vault_kv_secret_v2.datadog](https://registry.terraform.io/providers/hashicorp/vault/latest/docs/data-sources/kv_secret_v2) | data source |

## Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| <a name="input_additional_gateway_hosts"></a> [additional\_gateway\_hosts](#input\_additional\_gateway\_hosts) | Extra exact-host HTTPS listeners on the Envoy gateway, one per entry, for multi-tenant<br/>platforms where several hostnames land on the same install (the shared GovCloud platform<br/>serves ten). Each entry needs the authoritative hostname and its own TLS secret name.<br/><br/>The primary host stays gateway.hosts[0] (ingress\_hostname or dns\_domain\_name) and these<br/>append from index 1, which is deliberate: the chart derives $firstHost from hosts[0] and<br/>uses it both to decide whether to place the cert-manager cluster-issuer annotation on the<br/>Gateway (wildcard first host = no annotation) and to build the default alertmanager/grafana<br/>ops hostnames. Appending can therefore never disturb either.<br/><br/>Empty (the default) reproduces single-host behaviour exactly, so existing environments are<br/>unaffected.<br/><br/>On AWS with cert-manager, every entry gets a Certificate via the gateway-shim as soon as<br/>the Gateway exists, and HTTP-01 only succeeds once the hostname already resolves to this<br/>install's load balancer. When migrating hostnames onto a new stack, add them here at the<br/>DNS cutover rather than up front, or the failing ACME orders burn Let's Encrypt's<br/>per-hostname failed-validation budget. | <pre>list(object({<br/>    hostname        = string<br/>    tls_secret_name = string<br/>  }))</pre> | `[]` | no |
| <a name="input_alertmanager_slack_enabled"></a> [alertmanager\_slack\_enabled](#input\_alertmanager\_slack\_enabled) | Post warning/critical Alertmanager notifications to the fleet-wide Slack webhook. On by default for AWS stacks, which is what every real env wants. Set false for stacks that are short-lived or disposable (the upgrade-test harness does): their Alertmanager dies with the cluster at teardown, so the resolved notification never sends and the firing message stays in the channel for a cluster that no longer exists. | `bool` | `true` | no |
| <a name="input_alertmanager_slack_interactivity_enabled"></a> [alertmanager\_slack\_interactivity\_enabled](#input\_alertmanager\_slack\_interactivity\_enabled) | Enable the Slack Silence 2h action after the standalone signature-verified handler is deployed. The handler and Alertmanager reuse the fleet-global ops BasicAuth credential. | `bool` | `false` | no |
| <a name="input_app_image_flavor"></a> [app\_image\_flavor](#input\_app\_image\_flavor) | App image family: legacy (vm-in-a-can app repo) or slim (monolith-app family). Slim switches the chart's images.app.path to monolith-app and enables the dedicated beanstalkd image, so it also requires beanstalkd\_tag and chart\_version >= 2.11.0 (first chart with images.flavor support). | `string` | `"legacy"` | no |
| <a name="input_aws_external_dns_role_arn"></a> [aws\_external\_dns\_role\_arn](#input\_aws\_external\_dns\_role\_arn) | AWS IAM role ARN that external-dns assumes via AKS workload identity (azure). Empty = external-dns disabled. | `string` | `""` | no |
| <a name="input_aws_profile"></a> [aws\_profile](#input\_aws\_profile) | If running terraform from a workstation, which AWS CLI profile should we use for asset provisioning. | `string` | `""` | no |
| <a name="input_azure_acme_server"></a> [azure\_acme\_server](#input\_azure\_acme\_server) | ACME directory URL for the cert-issuer when azure\_tls\_mode=letsencrypt. Empty = chart default (LE prod). Use the staging URL during bring-up. | `string` | `""` | no |
| <a name="input_azure_environment"></a> [azure\_environment](#input\_azure\_environment) | Azure cloud environment: public or usgovernment. | `string` | `"public"` | no |
| <a name="input_azure_eso_identity_client_id"></a> [azure\_eso\_identity\_client\_id](#input\_azure\_eso\_identity\_client\_id) | Client ID of the ESO workload identity (physical output eso\_identity\_client\_id). | `string` | `""` | no |
| <a name="input_azure_key_vault_id"></a> [azure\_key\_vault\_id](#input\_azure\_key\_vault\_id) | Key Vault resource ID (physical output key\_vault\_id). | `string` | `""` | no |
| <a name="input_azure_key_vault_uri"></a> [azure\_key\_vault\_uri](#input\_azure\_key\_vault\_uri) | Key Vault URI for the ESO SecretStore (physical output key\_vault\_uri). | `string` | `""` | no |
| <a name="input_azure_kubelogin_login"></a> [azure\_kubelogin\_login](#input\_azure\_kubelogin\_login) | kubelogin --login mode: azurecli on a workstation, msi on an Azure VM, or workloadidentity for OIDC federation (Spacelift — reads AZURE\_FEDERATED\_TOKEN\_FILE / AZURE\_CLIENT\_ID / AZURE\_TENANT\_ID from the run env). | `string` | `"azurecli"` | no |
| <a name="input_azure_resource_group"></a> [azure\_resource\_group](#input\_azure\_resource\_group) | Resource group containing the AKS cluster and Key Vault. Required when cloud = azure. | `string` | `""` | no |
| <a name="input_azure_subscription_id"></a> [azure\_subscription\_id](#input\_azure\_subscription\_id) | Azure subscription ID. Required when cloud = azure. | `string` | `""` | no |
| <a name="input_azure_tenant_id"></a> [azure\_tenant\_id](#input\_azure\_tenant\_id) | Entra tenant ID (physical output tenant\_id). | `string` | `""` | no |
| <a name="input_azure_tls_mode"></a> [azure\_tls\_mode](#input\_azure\_tls\_mode) | Azure gateway TLS strategy: self-signed (dev), letsencrypt (cert-manager HTTP-01), or supplied (tls\_cert/tls\_key). | `string` | `"self-signed"` | no |
| <a name="input_beanstalkd_tag"></a> [beanstalkd\_tag](#input\_beanstalkd\_tag) | Tag for the dedicated beanstalkd fork image (repo <registry>/beanstalkd). Required when app\_image\_flavor is slim; ignored on legacy. | `string` | `""` | no |
| <a name="input_bi_database_credential_secret"></a> [bi\_database\_credential\_secret](#input\_bi\_database\_credential\_secret) | ARN to secret containing bi db credentials | `string` | `""` | no |
| <a name="input_chart_version"></a> [chart\_version](#input\_chart\_version) | Dozuki chart version pulled from the registry (oci://<image\_repository>/charts/dozuki). | `string` | `"2.10.11"` | no |
| <a name="input_cloud"></a> [cloud](#input\_cloud) | Cloud the physical layer runs on. | `string` | `"aws"` | no |
| <a name="input_cloudwatch_exporter_enabled"></a> [cloudwatch\_exporter\_enabled](#input\_cloudwatch\_exporter\_enabled) | Scrape the two EC2 EBS burst-credit CloudWatch metrics into Prometheus. On by default on AWS because nothing else in the cluster can see EBS exhaustion. Set false to opt a stack out of the small per-node CloudWatch bill; the physical IAM role and pod identity association stay, they just go unused. | `bool` | `true` | no |
| <a name="input_customer"></a> [customer](#input\_customer) | The customer name for resource names and tagging. This will also be the autogenerated subdomain. | `string` | `""` | no |
| <a name="input_customer_tls_externally_managed"></a> [customer\_tls\_externally\_managed](#input\_customer\_tls\_externally\_managed) | LEGACY: customer TLS where the cert+key were hand-seeded into Vault<br/>secret/<tenant>/<env>/tls out-of-band. Superseded by tls\_cert/tls\_key, which<br/>seed the same Vault path from Terraform (customer data must start in<br/>terraform inputs, not manual vault writes). Kept only until the last legacy<br/>env's cert is migrated into stack vars; do not use for new envs. Delivery is<br/>identical:<br/>ESO syncs the Vault path into tls-secret and the chart skips rendering it.<br/>Mutually exclusive with tls\_cert/tls\_key. | `bool` | `false` | no |
| <a name="input_db_migrations_active_deadline_seconds"></a> [db\_migrations\_active\_deadline\_seconds](#input\_db\_migrations\_active\_deadline\_seconds) | activeDeadlineSeconds for the chart's db-migrations Job. The chart ships 14400 (4h); this overrides it down to 1h, so 1h is the effective deadline everywhere. The Job exits as soon as migrations finish, so a higher ceiling costs nothing on small DBs, but a slow one (large snapshot-restored DB, version-jump migration) dies DeadlineExceeded, so raise it per-env before an exceptional migration. This is also the floor for flux\_upgrade\_timeout, since helm waits on this Job. | `number` | `3600` | no |
| <a name="input_db_resource_id"></a> [db\_resource\_id](#input\_db\_resource\_id) | Stable identifier of the primary database (physical db\_resource\_id output). Passed to the chart as db.resourceId so a DB replace re-runs migrations. Defaults empty; wired from physical via infra-live. Empty keeps the migration Job name tag-only (unchanged). | `string` | `""` | no |
| <a name="input_delete_after"></a> [delete\_after](#input\_delete\_after) | Optional RFC3339 timestamp. When set, the AWS EKS addon resource and dynamically provisioned EBS volumes (via the StorageClass tagSpecification) are tagged deleteAfter=<value> so the ResourceReaper janitor can purge them after that time if teardown fails. Empty = no tag (normal deploys). | `string` | `""` | no |
| <a name="input_dms_enabled"></a> [dms\_enabled](#input\_dms\_enabled) | If BI is enabled, whether or not to use DMS for conditional replication if true or a basic RDS read replica if false. | `bool` | `false` | no |
| <a name="input_dms_replication_generation"></a> [dms\_replication\_generation](#input\_dms\_replication\_generation) | Physical's hash of the replication-config attributes whose change stops the replication. Folded into the dms-start Job name so the modify that stops it also re-runs the starter. Empty (old physical layers) keeps the static Job name. | `string` | `""` | no |
| <a name="input_dms_task_arn"></a> [dms\_task\_arn](#input\_dms\_task\_arn) | If BI is enabled, the ARN of the BI replication. A DMS Serverless replication CONFIG on current physical layers, a provisioned replication TASK on older ones; the dms-start Job branches on the ARN's shape. Name kept as-is so consumers do not have to change. | `string` | `""` | no |
| <a name="input_dns_domain_name"></a> [dns\_domain\_name](#input\_dns\_domain\_name) | Auto-provisioned subdomain for this environment | `string` | n/a | yes |
| <a name="input_eks_cluster_id"></a> [eks\_cluster\_id](#input\_eks\_cluster\_id) | ID of EKS cluster for app provisioning | `string` | n/a | yes |
| <a name="input_enable_bi"></a> [enable\_bi](#input\_enable\_bi) | Whether to deploy resources for BI, a replica database, a DMS task, and a Kafka cluster | `bool` | `false` | no |
| <a name="input_enable_dashboards"></a> [enable\_dashboards](#input\_enable\_dashboards) | Turns on the dozuki chart's shared Grafana dashboards subchart (dashboards.enabled) and the dozuki-operator's per-subsite Grafana-org provisioning (dozuki-operator.grafana.url). Generates and seeds the "grafana" Vault/Key Vault secret (jwt signing secret + admin credentials) this layer's ESO ExternalSecrets read - no manual secret seeding required. Requires chart\_version >= 1.0.0 and the bundled dozuki-operator >= 4.0.0 (older pins silently no-op on dozuki-operator.grafana.url). | `bool` | `false` | no |
| <a name="input_enable_datadog"></a> [enable\_datadog](#input\_enable\_datadog) | Installs the Datadog agent (lean: APM trace intake + SSI library injection + continuous profiler only) and instruments the monolith PHP pods. Dozuki-internal observability for MPC stacks - never enable on CloudPrem customer installs. AWS only; reads the API key from Vault secret/dozuki/global/datadog. See datadog.tf. | `bool` | `false` | no |
| <a name="input_enable_primary_site_grafana"></a> [enable\_primary\_site\_grafana](#input\_enable\_primary\_site\_grafana) | Turns on the dozuki-operator's reconciliation of the PRIMARY site's Grafana org (dozuki-operator.grafana.primarySite). Subsite discovery covers only subsites, so without this the primary site's Grafana datasource is never reconciled and keeps whatever the last manual provisioning run left behind, which makes dashboard queries against it fail. Off by default; requires enable\_dashboards and a bundled dozuki-operator >= 4.1.0 (older pins silently ignore it). Enable per environment after validating on a non-production install: it has preconditions on that install's Grafana admin account, and the operator fails closed if a Subsite resource already targets the primary site rather than contending with the subsite controller over the same org. | `bool` | `false` | no |
| <a name="input_environment"></a> [environment](#input\_environment) | Environment of the application | `string` | `"dev"` | no |
| <a name="input_external_dns_sa_name"></a> [external\_dns\_sa\_name](#input\_external\_dns\_sa\_name) | external-dns service account name (must match the AWS role trust subject). | `string` | `"external-dns"` | no |
| <a name="input_flux_chart_version"></a> [flux\_chart\_version](#input\_flux\_chart\_version) | fluxcd-community/flux2 chart version for the app-delivery controllers. 2.19.0 = Flux 2.9.1 (helm-controller v1.6.2, Helm 4/SSA line), validated adopting the release cleanly on min under Helm 3 and 4. | `string` | `"2.19.0"` | no |
| <a name="input_flux_slack_bot_token"></a> [flux\_slack\_bot\_token](#input\_flux\_slack\_bot\_token) | Legacy Slack bot token for Flux's fixed native formatter. Ignored when flux\_slack\_relay\_enabled is true; the signed relay keeps the bot token out of customer clusters. | `string` | `""` | no |
| <a name="input_flux_slack_channel"></a> [flux\_slack\_channel](#input\_flux\_slack\_channel) | Slack channel ID for the legacy direct bot-token transport. The signed relay owns its channel server-side. | `string` | `""` | no |
| <a name="input_flux_slack_relay_enabled"></a> [flux\_slack\_relay\_enabled](#input\_flux\_slack\_relay\_enabled) | Route Flux events through the partition-local signed relay for golden Slack cards and deployment lifecycle threads. AWS only; the relay endpoint and HMAC token are projected from Vault by External Secrets. | `bool` | `false` | no |
| <a name="input_flux_slack_webhook_url"></a> [flux\_slack\_webhook\_url](#input\_flux\_slack\_webhook\_url) | Legacy Slack incoming-webhook URL for Flux notifications. Ignored when flux\_slack\_relay\_enabled is true. Empty leaves this fallback transport off. | `string` | `""` | no |
| <a name="input_flux_upgrade_timeout"></a> [flux\_upgrade\_timeout](#input\_flux\_upgrade\_timeout) | Per-attempt timeout for the dozuki HelmRelease's upgrade. The default is unchanged from what<br/>was hardcoded before, so leaving it alone is a no-op; envs that want a failed rollout to reach<br/>its terminal state sooner set it in env.hcl.<br/><br/>Why the default is this large: disableWait=false makes helm block on every resource in the<br/>release, including the db-migrations Job, so this must clear the slowest legitimate migration.<br/>Set it below that and an upgrade carrying a real migration gets cut off part way through. The<br/>floor is db\_migrations\_active\_deadline\_seconds (3600 today, so ~1h) plus headroom for pulls<br/>and pod readiness, NOT the chart's own 14400 default that CPI overrides. Raise both together<br/>before an exceptional migration.<br/><br/>Do not read a short timeout as "it just rolls back". remediation.retries=2 with<br/>remediateLastFailure=false means Flux rolls back between the first two failed attempts (old<br/>app image against a partly migrated schema) and then performs NO rollback on the third, so a<br/>terminal failure leaves the release failed with live state partially applied, pending operator<br/>recovery. Cutting a migration short is bad in either branch.<br/><br/>Why an env may still want it short: on an env with no large migration to run, a crashlooping<br/>rollout just burns the whole window before helm gives up (dev-slim, 2026-08-07, 04:07-08:37).<br/>retries=2 multiplies this by up to 3 for the full failure cycle. Detection does not depend on<br/>this value: FluxHelmReleaseStuck already fires after 30m of ready=Unknown.<br/><br/>Upgrade only. install keeps its own timeout: a fresh install always runs the full schema<br/>import, and install remediation uninstalls rather than rolls back. | `string` | `"4h30m"` | no |
| <a name="input_gateway_dns_label"></a> [gateway\_dns\_label](#input\_gateway\_dns\_label) | Azure DNS label for the gateway LoadBalancer (azure). Yields <label>.<region>.cloudapp.azure.com. Empty = LB public IP with no DNS label. | `string` | `""` | no |
| <a name="input_ghcr_pull_token"></a> [ghcr\_pull\_token](#input\_ghcr\_pull\_token) | GitHub token (read:packages) for pulling MPC images from GHCR (Azure only). | `string` | `""` | no |
| <a name="input_ghcr_pull_username"></a> [ghcr\_pull\_username](#input\_ghcr\_pull\_username) | GitHub username for pulling MPC images from GHCR (Azure only). | `string` | `""` | no |
| <a name="input_google_translate_api_token"></a> [google\_translate\_api\_token](#input\_google\_translate\_api\_token) | If using machine translation, enter your google translate API token here. | `string` | `""` | no |
| <a name="input_grafana_subpath"></a> [grafana\_subpath](#input\_grafana\_subpath) | Subpath to serve Grafana from | `string` | `"dashboards"` | no |
| <a name="input_image_repository"></a> [image\_repository](#input\_image\_repository) | Docker image repository (ECR) for app containers. | `string` | n/a | yes |
| <a name="input_image_tag"></a> [image\_tag](#input\_image\_tag) | Docker image tag for the main Dozuki app container. Changes with every deploy. | `string` | n/a | yes |
| <a name="input_ingress_hostname"></a> [ingress\_hostname](#input\_ingress\_hostname) | Hostname for the app ingress. Set to a wildcard (e.g. *.customer.com) for customer-provided certs. Defaults to dns\_domain\_name. | `string` | `""` | no |
| <a name="input_memcached_ascii_protocol"></a> [memcached\_ascii\_protocol](#input\_memcached\_ascii\_protocol) | Switch the app's cache client to the ASCII/text protocol (renders isMcRouterEnabled into memcached.json). Safe on its own against plain memcached, and REQUIRED before memcached\_proxy\_enabled because the built-in proxy is text-only. Phase B. | `bool` | `false` | no |
| <a name="input_memcached_proxy_backend_replicas"></a> [memcached\_proxy\_backend\_replicas](#input\_memcached\_proxy\_backend\_replicas) | Number of memcached cache backends behind the proxy. One by default: a second backend buys capacity, not availability, and CHANGING THIS ON A LIVE ENV IS NOT SAFE - the pool hashes with JumpHash, so the count is part of every key's identity and a change moves ownership mid-rollout. Resharding requires a deliberate cold-cache procedure; see the chart's values.yaml. | `number` | `1` | no |
| <a name="input_memcached_proxy_deploy"></a> [memcached\_proxy\_deploy](#input\_memcached\_proxy\_deploy) | Bring up the memcached backends StatefulSet and the proxy tier alongside the existing single memcached, taking no traffic. Phase A. | `bool` | `false` | no |
| <a name="input_memcached_proxy_enabled"></a> [memcached\_proxy\_enabled](#input\_memcached\_proxy\_enabled) | Flip the dozuki-memcached Service selector to the proxy tier and remove the single memcached Deployment. Requires memcached\_proxy\_deploy and memcached\_ascii\_protocol. Phase C. | `bool` | `false` | no |
| <a name="input_mimir_remote_write_enabled"></a> [mimir\_remote\_write\_enabled](#input\_mimir\_remote\_write\_enabled) | Ship a copy of this cluster's metrics to the central Mimir with Prometheus remote\_write. Purely additive: the local Prometheus keeps its full TSDB, its rules and its Alertmanager whether this is on or off, which makes flipping it back off a zero-impact revert. Needs two things first: enable\_mimir on the physical layer for the network path, and this env's ingest key seeded at <customer>/<environment>/mimir in Vault. Off by default until an env is deliberately rolled out. Silently a no-op unless this env's chart\_version is >= 2.7.0, the release that carries the remote\_write support: an older chart ignores the values and reports nothing. | `bool` | `false` | no |
| <a name="input_mimir_url"></a> [mimir\_url](#input\_mimir\_url) | Remote-write push endpoint. Commercial stacks resolve this name through the private hosted zone the physical layer creates over the PrivateLink endpoint; gov has no PrivateLink path and points at the public ingest name instead, so it overrides this. | `string` | `"https://mimir-int.dozuki.dev/api/v1/push"` | no |
| <a name="input_nextjs_extra_env"></a> [nextjs\_extra\_env](#input\_nextjs\_extra\_env) | Extra env vars for the web-nextjs deployment (name => value), e.g. per-env service API URLs. | `map(string)` | `{}` | no |
| <a name="input_nextjs_service_jwt_private_key"></a> [nextjs\_service\_jwt\_private\_key](#input\_nextjs\_service\_jwt\_private\_key) | web-nextjs service JWT signing key (Azure only; AWS syncs it into Vault from 1Password via infra-tf's vault-config). Seeded into the Key Vault 'nextjs' secret, which chart >= 1.9.0 reads unconditionally. | `string` | `""` | no |
| <a name="input_nextjs_tag"></a> [nextjs\_tag](#input\_nextjs\_tag) | Docker image tag for the Next.js frontend container. Changes with every deploy. | `string` | n/a | yes |
| <a name="input_nlb_http_target_group_arn"></a> [nlb\_http\_target\_group\_arn](#input\_nlb\_http\_target\_group\_arn) | NLB HTTP target group ARN for TargetGroupBinding | `string` | n/a | yes |
| <a name="input_nlb_https_target_group_arn"></a> [nlb\_https\_target\_group\_arn](#input\_nlb\_https\_target\_group\_arn) | NLB HTTPS target group ARN for TargetGroupBinding | `string` | n/a | yes |
| <a name="input_primary_db_secret"></a> [primary\_db\_secret](#input\_primary\_db\_secret) | ARN to secret containing primary db credentials | `string` | n/a | yes |
| <a name="input_protect_resources"></a> [protect\_resources](#input\_protect\_resources) | When true, retain Vault secrets on destroy (soft delete). When false, permanently purge all versions. | `bool` | `true` | no |
| <a name="input_rustici_managed_password"></a> [rustici\_managed\_password](#input\_rustici\_managed\_password) | Rustici managed password (Azure only; AWS reads Vault). | `string` | `""` | no |
| <a name="input_rustici_password"></a> [rustici\_password](#input\_rustici\_password) | Rustici password (Azure only; AWS reads Vault). | `string` | `""` | no |
| <a name="input_s3_documents_bucket"></a> [s3\_documents\_bucket](#input\_s3\_documents\_bucket) | Name of the bucket to store documents. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_images_bucket"></a> [s3\_images\_bucket](#input\_s3\_images\_bucket) | Name of the bucket to store guide images. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_kms_key_id"></a> [s3\_kms\_key\_id](#input\_s3\_kms\_key\_id) | AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN | `string` | `""` | no |
| <a name="input_s3_objects_bucket"></a> [s3\_objects\_bucket](#input\_s3\_objects\_bucket) | Name of the bucket to store guide objects. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_pdfs_bucket"></a> [s3\_pdfs\_bucket](#input\_s3\_pdfs\_bucket) | Name of the bucket to store guide pdfs. Use with 'create\_s3\_buckets' = false. | `string` | `""` | no |
| <a name="input_s3_replicate_buckets"></a> [s3\_replicate\_buckets](#input\_s3\_replicate\_buckets) | Whether or not we are replicating objects from existing S3 buckets. | `bool` | `false` | no |
| <a name="input_seaweedfs_volume_size_gb"></a> [seaweedfs\_volume\_size\_gb](#input\_seaweedfs\_volume\_size\_gb) | PVC size in GB for each SeaweedFS volume server (Azure only). | `number` | `100` | no |
| <a name="input_sentry_dsn"></a> [sentry\_dsn](#input\_sentry\_dsn) | Sentry DSN (Azure only; AWS reads Vault). | `string` | `""` | no |
| <a name="input_service_jwt_validation_key"></a> [service\_jwt\_validation\_key](#input\_service\_jwt\_validation\_key) | Monolith-side service JWT validation key (Azure only; AWS syncs it into Vault from 1Password via infra-tf's vault-config). Seeded into the Key Vault 'service-jwt' secret, which chart >= 1.12.0 reads unconditionally. Empty = verification disabled. | `string` | `""` | no |
| <a name="input_smtp_auth_enabled"></a> [smtp\_auth\_enabled](#input\_smtp\_auth\_enabled) | Whether to use SMTP authentication. | `bool` | `true` | no |
| <a name="input_smtp_enabled"></a> [smtp\_enabled](#input\_smtp\_enabled) | Whether to enable SMTP email sending. | `bool` | `true` | no |
| <a name="input_smtp_from_address"></a> [smtp\_from\_address](#input\_smtp\_from\_address) | SMTP from email address. | `string` | `"noreply@dozuki.com"` | no |
| <a name="input_smtp_host"></a> [smtp\_host](#input\_smtp\_host) | SMTP server hostname (and port if necessary). | `string` | `"smtp.sendgrid.net:587"` | no |
| <a name="input_smtp_password"></a> [smtp\_password](#input\_smtp\_password) | SMTP authentication password. Feeds the Azure Key Vault path (keyvault.tf) and the plain/onprem Flux values (flux.tf) only - the AWS Vault path no longer writes this to secret/<customer>/<env>/smtp (see the `removed` block in vault.tf). That change requires the dozuki chart release carrying the ExternalSecret null-safe smtp\_password guard (dozuki/helm#221) to be live fleet-wide before any env's Vault smtp path is deleted; on older chart versions an absent path aborts the whole dozuki-infra-credentials sync. | `string` | `""` | no |
| <a name="input_smtp_username"></a> [smtp\_username](#input\_smtp\_username) | SMTP authentication username. | `string` | `"apikey"` | no |
| <a name="input_spacelift"></a> [spacelift](#input\_spacelift) | Set to true when running in Spacelift. Enables IAM auth for the Vault provider. | `bool` | `false` | no |
| <a name="input_subsite_gateway_api_enabled"></a> [subsite\_gateway\_api\_enabled](#input\_subsite\_gateway\_api\_enabled) | Switches subsite routing to Gateway API HTTPRoutes: the dozuki-operator reconciles one HTTPRoute per subsite off the chart's dozuki-gateway instead of the legacy nginx Ingress, so subsites route automatically on Envoy Gateway installs (no hand-created wildcard HTTPRoutes). Off by default - it changes subsite routing behavior, so enable per-env after validating. Requires a bundled dozuki-operator >= the version that ships gatewayAPI.enabled (older pins silently ignore it). Safe on GovCloud's exact-host gateway layout (the operator no-ops there). | `bool` | `false` | no |
| <a name="input_surveyjs_license_key"></a> [surveyjs\_license\_key](#input\_surveyjs\_license\_key) | SurveyJS license key (Azure only; AWS reads Vault). | `string` | `""` | no |
| <a name="input_tls_cert"></a> [tls\_cert](#input\_tls\_cert) | Base64-encoded PEM TLS certificate (full chain) for the gateway. When set, cert-manager/ACME is bypassed. On AWS Terraform seeds it into Vault (secret/<customer>/<env>/tls) and ESO owns tls-secret; on azure/onprem the chart renders tls-secret. Empty = cert-manager/ACME (AWS) or azure\_tls\_mode (azure). Certs are public data and can live in env.hcl; keep tls\_key a masked stack TF\_VAR. | `string` | `""` | no |
| <a name="input_tls_key"></a> [tls\_key](#input\_tls\_key) | Base64-encoded PEM TLS private key matching tls\_cert. Required when tls\_cert is set. Set as a masked Spacelift TF\_VAR on the env's logical stack, not in git. | `string` | `""` | no |
| <a name="input_vault_address"></a> [vault\_address](#input\_vault\_address) | Vault server address accessible from within the cluster (PrivateLink). | `string` | n/a | yes |
| <a name="input_verify_artifact_pins"></a> [verify\_artifact\_pins](#input\_verify\_artifact\_pins) | Fail the plan when a pinned artifact (image\_tag, nextjs\_tag, chart\_version) does not exist in the env's ECR registry. Escape hatch for registries that revoke cross-account DescribeImages; see artifact-pins.tf. | `bool` | `true` | no |
| <a name="input_watchdog_heartbeat_enabled"></a> [watchdog\_heartbeat\_enabled](#input\_watchdog\_heartbeat\_enabled) | Route Alertmanager's Watchdog alert to the alert relay as a heartbeat. The relay recognizes alertname=Watchdog and records a CloudWatch datapoint instead of posting it, which is what lets a deadman alarm tell 'this environment stopped delivering alerts' apart from 'this environment has nothing to say'. Without it, an alert-delivery failure is indistinguishable from silence, and the alert that would report the failure has to travel the broken path to reach anyone. Off by default so a chart\_version bump never changes alert routing on its own. A no-op unless chart\_version is >= 2.8.0, the release that added the route. Turn this on and confirm heartbeat datapoints are arriving BEFORE arming the deadman alarm on the relay, or it pages immediately on missing data. | `bool` | `false` | no |
| <a name="input_webnextjs_env"></a> [webnextjs\_env](#input\_webnextjs\_env) | Extra container env vars for the web-nextjs pods, merged into the chart's webNextjs.env map. Per-env service URLs live here (CREATOR\_PRO\_SERVICE\_API\_URL, MARKETPLACE\_SERVICE\_API\_URL, ...); an empty map renders nothing. The chart's SERVER\_SIDE\_MONOLITH\_API\_URL default is preserved by the merge. | `map(string)` | `{}` | no |
| <a name="input_zendesk_jwt_signing_key"></a> [zendesk\_jwt\_signing\_key](#input\_zendesk\_jwt\_signing\_key) | Zendesk JWT signing key (Azure only; AWS syncs it into Vault from 1Password via infra-tf's vault-config). Seeded into the Key Vault 'zendesk' secret, which chart >= 1.13.0 reads unconditionally. | `string` | `""` | no |

## Outputs

| Name | Description |
| ---- | ----------- |
| <a name="output_dozuki_url"></a> [dozuki\_url](#output\_dozuki\_url) | URL to your Dozuki Installation. |
| <a name="output_grafana_admin_password"></a> [grafana\_admin\_password](#output\_grafana\_admin\_password) | Password for Grafana admin user |
| <a name="output_grafana_admin_username"></a> [grafana\_admin\_username](#output\_grafana\_admin\_username) | n/a |
| <a name="output_grafana_url"></a> [grafana\_url](#output\_grafana\_url) | n/a |
| <a name="output_ingress_ip"></a> [ingress\_ip](#output\_ingress\_ip) | Public IP of the ingress load balancer (Azure only; point DNS here). |
| <a name="output_replicate_instructions"></a> [replicate\_instructions](#output\_replicate\_instructions) | n/a |
<!-- END_TF_DOCS -->
