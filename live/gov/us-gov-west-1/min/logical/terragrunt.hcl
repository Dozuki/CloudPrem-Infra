skip = get_env("SKIP_LOGICAL", false)
# Terragrunt will copy the Terraform configurations specified by the source parameter, along with any files in the
# working directory, into a temporary folder, and execute your Terraform commands in that folder.
terraform {
  source = "../../../../../cloudprem//logical"
}

# Include all settings from the root terragrunt.hcl file
include {
  path = find_in_parent_folders()
}

dependency "physical" {
  config_path = "../physical"

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
  }
  mock_outputs_allowed_terraform_commands = ["validate","destroy"]
}

retryable_errors = [
  "(?s).*frontegg-db-update is in failed state.*"
]

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
  s3_images_bucket = dependency.physical.outputs.guide_images_bucket
  s3_objects_bucket = dependency.physical.outputs.guide_objects_bucket
  s3_documents_bucket = dependency.physical.outputs.documents_bucket
  s3_pdfs_bucket = dependency.physical.outputs.guide_pdfs_bucket
  memcached_cluster_address = dependency.physical.outputs.memcached_cluster_address
  dms_task_arn = dependency.physical.outputs.dms_task_arn
}