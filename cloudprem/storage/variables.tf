variable "identifier" {
  description = "A name identifier to use as prefix for all the resources."
  type        = string
  default     = ""

  validation {
    condition     = length(var.identifier) <= 10
    error_message = "The length of the identifier must be less than 11 characters."
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
variable "protect_resources" {
  description = "Specifies whether data protection settings are enabled. If true they will prevent stack deletion until protections have been manually disabled."
  type        = bool
  default     = true
}
variable "vpc_id" {
  description = "The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private"
  type        = string
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

variable "rds_kms_key_id" {
  description = "AWS KMS key identifier for RDS encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN"
  type        = string
  default     = "alias/aws/rds"
}

variable "create_s3_buckets" {
  description = "Wheter to create the dozuki S3 buckets or not."
  type        = bool
  default     = true
}

variable "rds_snapshot_identifier" {
  description = "We can seed the database from an existing RDS snapshot in this region. Type the snapshot identifier in this field or leave blank to start with a fresh database. Note: If you do use a snapshot it's critical that during stack updates you continue to include the snapshot identifier in this parameter. Clearing this parameter after using it will cause AWS to spin up a new fresh DB and delete your old one."
  type        = string
  default     = ""
}

variable "rds_instance_type" {
  description = "The instance type to use for your database. See this page for a breakdown of the performance and cost differences between the different instance types: https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.DBInstanceClass.html"
  type        = string
  default     = "db.m4.large"
}

variable "rds_multi_az" {
  description = "If true we will tell RDS to automatically deploy and manage a highly available standby instance of your database. Enabling this doubles the cost of the RDS instance but without it you are susceptible to downtime if the AWS availability zone your RDS instance is in becomes unavailable."
  type        = bool
  default     = true
}

variable "rds_allocated_storage" {
  description = "The initial size of the database (Gb)"
  type        = number
  default     = 100

  validation {
    condition     = var.rds_allocated_storage > 5 && var.rds_allocated_storage < 1000
    error_message = "The RDS allocated storage must be between 5 and 1000 Gb."
  }
}

variable "rds_max_allocated_storage" {
  description = "The maximum size to which AWS will scale the database (Gb)"
  type        = number
  default     = 500

  validation {
    condition     = var.rds_max_allocated_storage > 5 && var.rds_max_allocated_storage < 1000
    error_message = "The RDS max allocated storage must be between 5 and 1000 Gb."
  }
}

variable "rds_backup_retention_period" {
  description = "The number of days to keep automatic database backups. Setting this value to 0 disables automatic backups."
  type        = number
  default     = 30

  validation {
    condition     = var.rds_backup_retention_period >= 0 && var.rds_backup_retention_period <= 35
    error_message = "AWS limits backup retention to 35 days max."
  }
}

variable "enable_bi" {
  description = "This option will spin up a BI slave of your master database and enable conditional replication (everything but the mysql table will be replicated so you can have custom users)."
  type        = bool
  default     = false
}
variable "public_access" {
  description = "Should the app and dashboard be accessible via a publicly routable IP and domain?"
  type        = bool
  default     = true
}

variable "elasticache_instance_type" {
  type        = string
  default     = "cache.t2.micro"
  description = "Elastic cache instance type"
}

variable "elasticache_cluster_size" {
  type        = number
  default     = 1
  description = "Cluster size"
}
variable "eks_cluster_id" {
  description = "ID of EKS cluster for app provisioning"
  type        = string
}
variable "eks_cluster_access_role_arn" {
  description = "ARN for cluster access role for app provisioning"
  type        = string
}