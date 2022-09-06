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

generate "webhooks_default_helmignore" {
  path      = "charts/connectivity/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "webhooks_event_helmignore" {
  path      = "charts/connectivity/charts/event-service/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "webhooks_api_helmignore" {
  path      = "charts/connectivity/charts/api-gateway/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "webhooks_webhook_helmignore" {
  path      = "charts/connectivity/charts/webhook-service/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "webhooks_connectors_helmignore" {
  path      = "charts/connectivity/charts/connectors-worker/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "webhooks_integrations_helmignore" {
  path      = "charts/connectivity/charts/integrations-service/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "webhooks_mongodb_helmignore" {
  path      = "charts/mongodb/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "webhooks_redis_helmignore" {
  path      = "charts/redis/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "aws_node_termination_handler_helmignore" {
  path      = "charts/aws-node-termination-handler/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "cluster_autoscaler_helmignore" {
  path      = "charts/cluster-autoscaler/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "metrics_server_helmignore" {
  path      = "charts/metrics-server/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
generate "grafana_helmignore" {
  path      = "charts/grafana/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}

dependency "physical" {
  config_path = "${get_terragrunt_dir()}/../physical"

  mock_outputs = {
    vpc_id = "temporary-dummy-id"
    azs_count = 3
    msk_bootstrap_brokers = "bootstrap-brokers"
    eks_worker_asg_arns = "dummy-arn1,dummy-arn2"
    eks_worker_asg_names = "dummy-name1,dummy-name2"
    eks_cluster_id = "dummy-cluster-id"
    eks_cluster_access_role_arn = "dummy-arn"
    eks_oidc_cluster_access_role_name = "dummy-ca-role-arn"
    termination_handler_role_arn = "dummy-termination-handler-role-arn"
    termination_handler_sqs_queue_id = "dummy-sqs-id"
    nlb_dns_name = "dummy-lb-dns"
    cluster_primary_sg = "dummy-sg"
    primary_db_secret = "dummy-secret-id"
    guide_images_bucket = "dummy-images-bucket"
    guide_objects_bucket = "dummy-objects-bucket"
    documents_bucket = "dummy-documents-bucket"
    guide_pdfs_bucket = "dummy-pdfs-bucket"
    memcached_cluster_address = "dummy-memcache"
    dms_task_arn = "dummy-dms-arn"
    bi_database_credential_secret = "dummy-secret"
  }
  mock_outputs_merge_strategy_with_state = "shallow"
}

inputs = {
  vpc_id = dependency.physical.outputs.vpc_id
  azs_count = dependency.physical.outputs.azs_count

  msk_bootstrap_brokers = dependency.physical.outputs.msk_bootstrap_brokers

  eks_cluster_id = dependency.physical.outputs.eks_cluster_id
  eks_cluster_access_role_arn = dependency.physical.outputs.eks_cluster_access_role_arn
  eks_oidc_cluster_access_role_name = dependency.physical.outputs.eks_oidc_cluster_access_role_name
  termination_handler_role_arn = dependency.physical.outputs.termination_handler_role_arn
  termination_handler_sqs_queue_id = dependency.physical.outputs.termination_handler_sqs_queue_id
  eks_worker_asg_arns = dependency.physical.outputs.eks_worker_asg_arns
  eks_worker_asg_names = dependency.physical.outputs.eks_worker_asg_names
  nlb_dns_name = dependency.physical.outputs.nlb_dns_name
  cluster_primary_sg = dependency.physical.outputs.cluster_primary_sg

  primary_db_secret = dependency.physical.outputs.primary_db_secret
  bi_database_credential_secret = dependency.physical.outputs.bi_database_credential_secret
  s3_images_bucket = dependency.physical.outputs.guide_images_bucket
  s3_objects_bucket = dependency.physical.outputs.guide_objects_bucket
  s3_documents_bucket = dependency.physical.outputs.documents_bucket
  s3_pdfs_bucket = dependency.physical.outputs.guide_pdfs_bucket
  memcached_cluster_address = dependency.physical.outputs.memcached_cluster_address
  dms_task_arn = dependency.physical.outputs.dms_task_arn
}