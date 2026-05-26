locals {
  helmignore = <<EOF
.DS_Store
# Common VCS dirs
.git/
.gitignore
.bzr/
.bzrignore
.hg/
.hgignore
.svn/
# Common backup files
*.swp
*.bak
*.tmp
*.orig
*~
# Various IDEs
.project
.idea/
*.tmproj
.vscode/
.terragrunt-source-manifest
.terragrunt-source-manifest/
  EOF
}

generate "dozuki_helmignore" {
  path      = "charts/dozuki/chart/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "metrics_server_helmignore" {
  path      = "charts/metrics-server/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
dependency "physical" {
  config_path = "${get_terragrunt_dir()}/../physical"

  mock_outputs = {
    vpc_id = "temporary-dummy-id"
    azs_count = 3
    msk_bootstrap_brokers = "bootstrap-brokers"
    eks_cluster_id = "dummy-cluster-id"
    eks_cluster_access_role_arn = "dummy-arn"
    dns_domain_name = "dummy-lb-dns"
    cluster_primary_sg = "dummy-sg"
    primary_db_secret = "dummy-secret-id"
    guide_images_bucket = "dummy-images-bucket"
    guide_objects_bucket = "dummy-objects-bucket"
    documents_bucket = "dummy-documents-bucket"
    guide_pdfs_bucket = "dummy-pdfs-bucket"
    s3_kms_key_id = "dummy-kms-arn"
    s3_replicate_buckets = "false"
    memcached_cluster_address = "dummy-memcache"
    dms_task_arn = "dummy-dms-arn"
    bi_database_credential_secret = "dummy-secret"
    dms_enabled = "false"
    eks_oidc_issuer_url = "https://oidc.eks.us-east-1.amazonaws.com/id/DUMMY"
    private_subnet_ids = ["subnet-dummy-a", "subnet-dummy-b", "subnet-dummy-c"]
    vault_endpoint_dns = "vault.internal.dozuki.com"
    nlb_https_target_group_arn = "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/dummy/dummy"
    nlb_http_target_group_arn = "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/dummy/dummy"
  }
  mock_outputs_merge_strategy_with_state = "shallow"
}

inputs = {
  vpc_id = dependency.physical.outputs.vpc_id
  azs_count = dependency.physical.outputs.azs_count

  msk_bootstrap_brokers = dependency.physical.outputs.msk_bootstrap_brokers

  eks_cluster_id = dependency.physical.outputs.eks_cluster_id
  eks_cluster_access_role_arn = dependency.physical.outputs.eks_cluster_access_role_arn
  dns_domain_name = dependency.physical.outputs.dns_domain_name
  cluster_primary_sg = dependency.physical.outputs.cluster_primary_sg

  primary_db_secret = dependency.physical.outputs.primary_db_secret
  bi_database_credential_secret = dependency.physical.outputs.bi_database_credential_secret
  s3_images_bucket = dependency.physical.outputs.guide_images_bucket
  s3_objects_bucket = dependency.physical.outputs.guide_objects_bucket
  s3_documents_bucket = dependency.physical.outputs.documents_bucket
  s3_pdfs_bucket = dependency.physical.outputs.guide_pdfs_bucket
  s3_kms_key_id = dependency.physical.outputs.s3_kms_key_id
  s3_replicate_buckets = dependency.physical.outputs.s3_replicate_buckets
  memcached_cluster_address = dependency.physical.outputs.memcached_cluster_address
  dms_task_arn = dependency.physical.outputs.dms_task_arn
  dms_enabled = dependency.physical.outputs.dms_enabled
  vault_address = "http://${dependency.physical.outputs.vault_endpoint_dns}:8200"
  nlb_https_target_group_arn = dependency.physical.outputs.nlb_https_target_group_arn
  nlb_http_target_group_arn = dependency.physical.outputs.nlb_http_target_group_arn
}