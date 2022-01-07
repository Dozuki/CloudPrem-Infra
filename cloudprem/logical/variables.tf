variable "identifier" {
  description = "A name identifier to use as prefix for all the resources."
  type        = string
  default     = ""

  validation {
    condition     = length(var.identifier) <= 10
    error_message = "The length of the identifier must be less than 11 characters."
  }
}
variable "dozuki_license_parameter_name" {
  description = "Parameter name for dozuki license in AWS Parameter store."
  default     = ""
}
variable "environment" {
  description = "Environment of the application"
  type        = string
  default     = "dev"

  validation {
    condition     = length(var.environment) <= 5
    error_message = "The length of the Environment must be less than 6 characters."
  }
}
variable "vpc_id" {
  description = "The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private"
  type        = string
  default     = ""
}
variable "azs_count" {
  default     = 3
  description = "The number of availability zones we should use for deployment."

  validation {
    condition     = var.azs_count >= 3 && var.azs_count <= 10
    error_message = "AZ count must be between 3 and 10."
  }
}
variable "eks_cluster_id" {
  description = "ID of EKS cluster for app provisioning"
  type        = string
}
variable "eks_cluster_access_role_arn" {
  description = "ARN for cluster access role for app provisioning"
  type        = string
}
variable "eks_oidc_cluster_access_role_name" {
  description = "ARN for OIDC-compatible IAM Role for the EKS Cluster Autoscaler"
  type        = string
}
variable "eks_worker_asg_arns" {
  description = "Autoscaling group ARNS for the EKS cluster"
  type        = list(string)
}
variable "eks_worker_asg_names" {
  description = "Autoscaling group names for the EKS cluster"
  type        = list(string)
}
variable "termination_handler_role_arn" {
  description = "IAM Role for EKS node termination handler"
}
variable "termination_handler_sqs_queue_id" {
  description = "SQS Queue ID for the EKS node termination handler"
}
variable "cluster_primary_sg" {
  description = "Primary Security Group for the EKS cluster, used for ingress SG source"
}
variable "primary_db_secret" {
  description = "ARN to secret containing primary db credentials"
  type        = string
}
variable "enable_webhooks" {
  description = "This option will spin up a managed Kafka & Redis cluster to support private webhooks."
  type        = bool
  default     = false
}
variable "nlb_dns_name" {
  description = "DNS address of the network load balancer and URL to the deployed application"
}
variable "replicated_app_sequence_number" {
  description = "For fresh installs you can target a specific Replicated sequence for first install. This will not be respected for existing installations. Use 0 for latest release."
  default     = 0
  type        = number
}
variable "memcached_cluster_address" {
  description = "Address of the deployed memcached cluster"
}
variable "s3_kms_key_id" {
  description = "AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN"
  type        = string
  default     = "alias/aws/s3"
}
variable "s3_objects_bucket" {
  description = "Name of the bucket to store guide objects. Use with 'create_s3_buckets' = false."
  type        = string
  default     = ""
}
variable "s3_images_bucket" {
  description = "Name of the bucket to store guide images. Use with 'create_s3_buckets' = false."
  type        = string
  default     = ""
}
variable "s3_documents_bucket" {
  description = "Name of the bucket to store documents. Use with 'create_s3_buckets' = false."
  type        = string
  default     = ""
}
variable "s3_pdfs_bucket" {
  description = "Name of the bucket to store guide pdfs. Use with 'create_s3_buckets' = false."
  type        = string
  default     = ""
}
variable "msk_bootstrap_brokers" {
  description = "Kafka bootstrap broker list"
}
#tfsec:ignore:general-secrets-sensitive-in-variable
variable "google_translate_api_token" {
  description = "If using machine translation, enter your google translate API token here."
  type        = string
  default     = ""
}
variable "enable_bi" {
  description = "Whether to deploy resources for BI, a replica database, a DMS task, and a Kafka cluster"
  type        = string
  default     = false
}
variable "dms_task_arn" {
  description = "If BI is enabled, the DMS replication task arn."
}
variable "aws_profile" {
  description = "If running terraform from a workstation, which AWS CLI profile should we use for asset provisioning."
  type        = string
  default     = ""
}