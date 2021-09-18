locals {
  # Automatically load environment-level variables
  environment_vars = read_terragrunt_config(find_in_parent_folders("env.hcl"))

  # Extract out common variables for reuse
  env = local.environment_vars.locals.environment
}

# Terragrunt will copy the Terraform configurations specified by the source parameter, along with any files in the
# working directory, into a temporary folder, and execute your Terraform commands in that folder.
terraform {
  source = "../../../../../cloudprem//app"
}

# Include all settings from the root terragrunt.hcl file
include {
  path = find_in_parent_folders()
}

dependency "network" {
  config_path = "../network"

  mock_outputs = {
    vpc_id = "temporary-dummy-id"
    azs_count = 3
  }
  mock_outputs_allowed_terraform_commands = ["validate"]
}
dependency "compute" {
  config_path = "../compute"

  mock_outputs = {
    eks_cluster_id = "dummy-cluster-id"
    eks_cluster_access_role_arn = "dummy-arn"
    nlb_dns_name = "dummy-lb-dns"
    cluster_primary_sg = "dummy-sg"
  }
  mock_outputs_allowed_terraform_commands = ["validate"]
}
dependency "storage" {
  config_path = "../storage"

  mock_outputs = {
    primary_db_secret = "dummy-secret-id"
    guide_images_bucket = "dummy-images-bucket"
    guide_objects_bucket = "dummy-objects-bucket"
    documents_bucket = "dummy-documents-bucket"
    guide_pdfs_bucket = "dummy-pdfs-bucket"
    memcached_cluster_address = "dummy-memcache"
  }
  mock_outputs_allowed_terraform_commands = ["validate"]
}
retryable_errors = [
  "(?s).*frontegg-db-update is in failed state.*"
]

inputs = {
  dozuki_license_parameter_name = local.environment_vars.locals.dozuki_license_parameter_name
  vpc_id = dependency.network.outputs.vpc_id
  azs_count = dependency.network.outputs.azs_count

  eks_cluster_id = dependency.compute.outputs.eks_cluster_id
  eks_cluster_access_role_arn = dependency.compute.outputs.eks_cluster_access_role_arn
  nlb_dns_name = dependency.compute.outputs.nlb_dns_name
  cluster_primary_sg = dependency.compute.outputs.cluster_primary_sg

  primary_db_secret = dependency.storage.outputs.primary_db_secret
  s3_images_bucket = dependency.storage.outputs.guide_images_bucket
  s3_objects_bucket = dependency.storage.outputs.guide_objects_bucket
  s3_documents_bucket = dependency.storage.outputs.documents_bucket
  s3_pdfs_bucket = dependency.storage.outputs.guide_pdfs_bucket
  memcached_cluster_address = dependency.storage.outputs.memcached_cluster_address
}