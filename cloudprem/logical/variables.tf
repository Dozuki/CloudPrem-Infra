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
  type        = string
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
variable "azs_count" {
  default     = 3
  type        = number
  description = "The number of availability zones we should use for deployment."

  validation {
    condition     = var.azs_count >= 3 && var.azs_count <= 10
    error_message = "AZ count must be between 3 and 10."
  }
}

variable "grafana_ssl_cn" {
  description = "If a custom common name is required for the self-signed SSL certificate for Grafana (if BI is enabled), set it here."
  type = string
  default = ""
}

variable "grafana_use_replicated_ssl" {
  description = "If true the Grafana installation will use the same SSL cert uploaded (or generated by) to the Replicated dashboard."
  type = bool
  default = true
}

variable "eks_cluster_id" {
  description = "ID of EKS cluster for app provisioning"
  type        = string
}
variable "eks_oidc_cluster_access_role_name" {
  description = "ARN for OIDC-compatible IAM Role for the EKS Cluster Autoscaler"
  type        = string
}
variable "eks_worker_asg_names" {
  description = "Autoscaling group names for the EKS cluster"
  type        = list(string)
}
variable "termination_handler_role_arn" {
  description = "IAM Role for EKS node termination handler"
  type        = string
}
variable "termination_handler_sqs_queue_id" {
  description = "SQS Queue ID for the EKS node termination handler"
  type        = string
}
variable "primary_db_secret" {
  description = "ARN to secret containing primary db credentials"
  type        = string
}
variable "bi_database_credential_secret" {
  description = "ARN to secret containing bi db credentials"
  type        = string
  default     = ""
}
variable "enable_webhooks" {
  description = "This option will spin up a managed Kafka & Redis cluster to support private webhooks."
  type        = bool
  default     = false
}
variable "nlb_dns_name" {
  type        = string
  description = "DNS address of the network load balancer and URL to the deployed application"
}
variable "replicated_app_sequence_number" {
  description = "For fresh installs you can target a specific Replicated sequence for first install. This will not be respected for existing installations. Use 0 for latest release."
  default     = 0
  type        = number
}
variable "replicated_channel" {
  description = "If specifying an app sequence for a fresh install, this is the channel that sequence was deployed to. You only need to set this if the sequence you configured was not released on the default channel associated with your customer license."
  default     = ""
  type        = string
}
variable "memcached_cluster_address" {
  type        = string
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
# This needs to have no type due to terraform's weird handling of string lists. If you set a type it will convert it to
# a format we can't feed into helm
# tflint-ignore: terraform_typed_variables
variable "msk_bootstrap_brokers" {
  description = "Kafka bootstrap broker list"
}
#tfsec:ignore:general-secrets-no-plaintext-exposure
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
  type        = string
  description = "If BI is enabled, the DMS replication task arn."
}
variable "aws_profile" {
  description = "If running terraform from a workstation, which AWS CLI profile should we use for asset provisioning."
  type        = string
  default     = ""
}