# Physical → Logical Contract

The logical layer consumes physical-layer outputs. On AWS this wiring lives in
`live/common.hcl` (terragrunt dependency block). On Azure it lives in the
`mpc-azure-config` boilerplate. A physical layer for any cloud MUST
provide the values below; "N/A" values are wired as literal constants in the
config layer, not produced by the cloud layer.

| Logical input | Meaning | AWS source (`terraform/physical` output) | Azure source (`terraform/physical-azure` output) |
|---|---|---|---|
| `eks_cluster_id` | K8s cluster name | `eks_cluster_id` | `cluster_name` |
| `dns_domain_name` | App FQDN | `dns_domain_name` (Route53-managed) | `dns_domain_name` (passthrough of `external_fqdn`; customer-managed DNS) |
| `primary_db_secret` | Locator of DB credentials secret | `primary_db_secret` (Secrets Manager ARN) | `db_credentials_secret_id` (Key Vault secret versionless ID) |
| `memcached_cluster_address` | Cache endpoint | `memcached_cluster_address` (ElastiCache) | N/A — logical deploys in-cluster memcached when `cloud = "azure"` and uses its Service DNS name |
| `s3_images_bucket` | Guide images bucket | `guide_images_bucket` (S3) | `guide_images_bucket` (SeaweedFS bucket name; created by logical) |
| `s3_objects_bucket` | Guide objects bucket | `guide_objects_bucket` | `guide_objects_bucket` |
| `s3_pdfs_bucket` | Guide PDFs bucket | `guide_pdfs_bucket` | `guide_pdfs_bucket` |
| `s3_documents_bucket` | Documents bucket | `documents_bucket` | `documents_bucket` |
| `s3_kms_key_id` | Object-store KMS key | `s3_kms_key_id` | N/A — `""` (SeaweedFS volumes ride Azure Storage encryption at rest) |
| `s3_replicate_buckets` | Migration-from-existing-buckets flag | `s3_replicate_buckets` | N/A — `false` |
| `vpc_id` | Network ID † | `vpc_id` | `vnet_id` |
| `azs_count` | AZ count † | `azs_count` | N/A — `3` (zonal layout is Azure-internal) |
| `vault_address` | HashiCorp Vault address (secret backend) | `"http://" + vault_endpoint_dns + ":8200"` | N/A — Azure uses Key Vault via ESO (`key_vault_uri`) |
| `msk_bootstrap_brokers` | Kafka brokers (webhooks) | `msk_bootstrap_brokers` | N/A — `""` (webhooks unsupported on Azure; `enable_webhooks = false`) |
| `dms_task_arn` / `dms_enabled` | BI replication | `dms_task_arn` / `dms_enabled` | N/A — `""` / `false` (BI deferred past Azure v1) |
| `bi_database_credential_secret` | BI DB secret | `bi_database_credential_secret` | N/A — `""` |
| `nlb_https_target_group_arn` | LB binding (HTTPS) | `nlb_https_target_group_arn` | N/A — `""` (Azure: Envoy Gateway uses a `LoadBalancer` Service; Azure cloud controller provisions the LB) |
| `nlb_http_target_group_arn` | LB binding (HTTP) | `nlb_http_target_group_arn` | N/A — `""` |
| `eks_cluster_access_role_arn` | Cluster access IAM role † | `eks_cluster_access_role_arn` | N/A — `""` (Azure auth is via `az aks get-credentials` / kubelogin) |
| `cluster_primary_sg` | Cluster primary SG † | `cluster_primary_sg` | N/A — `""` |
| `private_subnet_ids` | Private subnets † | `private_subnet_ids` | `aks_subnet_id` (single-element list semantics differ; logical does not consume this on Azure) |

† Wired in `live/common.hcl` but not declared in `terraform/logical/variables.tf` —
the logical layer does not consume these today (Terraform ignores undeclared
`TF_VAR_*`). Azure config wiring may omit them; they are documented for parity
with the AWS terragrunt wiring only.

## Azure-only outputs (consumed by logical when `cloud = "azure"`)

| Output | Meaning |
|---|---|
| `resource_group_name` | Resource group containing all resources |
| `location` | Azure region |
| `tenant_id` | Entra tenant ID (ESO ClusterSecretStore config) |
| `key_vault_uri` | Key Vault URI for ESO backend |
| `key_vault_id` | Key Vault resource ID |
| `eso_identity_client_id` | Client ID of the workload-identity UAI that ESO's service account annotates |
| `cluster_oidc_issuer_url` | AKS OIDC issuer (federated credentials) |
| `node_resource_group` | AKS-managed resource group (where the cloud controller puts LBs/disks) |
| `db_host` | MySQL Flexible Server FQDN |

## Secret shape

`db_credentials_secret_id` / `primary_db_secret` must resolve to a JSON
document with keys: `host`, `port`, `username`, `password`. (AWS: Secrets
Manager secret created by `terraform/physical` rds.tf; Azure: Key Vault secret
`database-credentials`.)
