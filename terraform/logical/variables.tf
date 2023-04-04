# --- BEGIN General Configuration --- #

variable "customer" {
  description = "The customer name for resource names and tagging. This will also be the autogenerated subdomain."
  type        = string
  default     = ""

  validation {
    condition = (
      length(var.customer) == 0 ||
      (length(var.customer) <= 10 &&
        can(regex("^[a-z0-9]", var.customer)) &&
        can(regex("[a-z0-9-]+", var.customer)) &&
        can(regex("[a-z0-9]$", var.customer))
    ))
    error_message = "Subdomain must be empty or between 1 and 10 characters long, start with a letter or digit, contain only lowercase letters, digits, or hyphens, and end with a letter or digit."
  }
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

variable "aws_profile" {
  description = "If running terraform from a workstation, which AWS CLI profile should we use for asset provisioning."
  type        = string
  default     = ""
}
# --- END General Configuration --- #

# --- BEGIN Network Configuration --- #

variable "azs_count" {
  default     = 3
  type        = number
  description = "The number of availability zones we should use for deployment."

  validation {
    condition     = var.azs_count >= 3 && var.azs_count <= 10
    error_message = "AZ count must be between 3 and 10."
  }
}

# --- END Network Configuration --- #

# --- BEGIN App Configuration --- #

variable "dozuki_customer_id_parameter_name" {
  type        = string
  description = "Parameter name for dozuki customer id in AWS Parameter store."
  default     = ""
}

variable "enable_webhooks" {
  description = "This option will spin up a managed Kafka & Redis cluster to support private webhooks."
  type        = bool
  default     = false
}

variable "enable_bi" {
  description = "Whether to deploy resources for BI, a replica database, a DMS task, and a Kafka cluster"
  type        = string
  default     = false
}

variable "replicated_channel" {
  description = "If specifying an app sequence for a fresh install, this is the channel that sequence was deployed to. You only need to set this if the sequence you configured was not released on the default channel associated with your customer license."
  default     = ""
  type        = string
}

#tfsec:ignore:general-secrets-no-plaintext-exposure
variable "google_translate_api_token" {
  description = "If using machine translation, enter your google translate API token here."
  type        = string
  default     = ""
}

# --- END App Configuration --- #

# --- BEGIN Physical Module Passthrough Configuration (do not set or modify) --- #

variable "eks_cluster_id" {
  description = "ID of EKS cluster for app provisioning"
  type        = string
}

variable "eks_oidc_cluster_access_role_name" {
  description = "ARN for OIDC-compatible IAM Role for the EKS Cluster Autoscaler"
  type        = string
}

variable "eks_cluster_access_role_arn" {
  description = "ARN for the IAM Role for API-based EKS cluster access."
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

variable "dns_domain_name" {
  type        = string
  description = "Auto-provisioned subdomain for this environment"
}

variable "memcached_cluster_address" {
  type        = string
  description = "Address of the deployed memcached cluster"
}

variable "s3_kms_key_id" {
  description = "AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN"
  type        = string
  default     = ""
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

variable "s3_replicate_buckets" {
  description = "Whether or not we are replicating objects from existing S3 buckets."
  type        = bool
  default     = false
}

# This needs to have no type due to terraform's weird handling of string lists. If you set a type it will convert it to
# a format we can't feed into helm
# tflint-ignore: terraform_typed_variables
variable "msk_bootstrap_brokers" {
  description = "Kafka bootstrap broker list"
}

variable "dms_task_arn" {
  type        = string
  description = "If BI is enabled, the DMS replication task arn."
}
# --- END Physical Module Passthrough Configuration (do not set or modify) --- #