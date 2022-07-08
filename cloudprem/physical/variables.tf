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

variable "vpc_cidr" {
  description = "The CIDR block for the VPC"
  type        = string
  default     = "172.16.0.0/16"
}

variable "highly_available_nat_gateway" {
  description = "Should be true if you want to provision a highly available NAT Gateway across all of your private networks"
  type        = bool
  default     = true
}
variable "azs_count" {
  description = "The number of availability zones we should use for deployment."
  type        = number
  default     = 3

  validation {
    condition     = var.azs_count >= 3 && var.azs_count <= 10
    error_message = "AZ count must be between 3 and 10."
  }
}
variable "s3_kms_key_id" {
  description = "AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN"
  type        = string
  default     = "alias/aws/s3"
}

variable "create_s3_buckets" {
  description = "Wheter to create the dozuki S3 buckets or not."
  type        = bool
  default     = true
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
variable "s3_logging_bucket" {
  description = "Name of the bucket to store bucket object access logs. Use with 'create_s3_buckets' = false."
  type        = string
  default     = ""
}

variable "rds_kms_key_id" {
  description = "AWS KMS key identifier for RDS encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN"
  type        = string
  default     = "alias/aws/rds"
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
variable "bi_public_access" {
  description = "NOTE: This is mutually exclusive with VPN access, both cannot be enabled at the same time. If BI is enabled and you need access to the BI database server from outside the amazon network, set this to true."
  type        = bool
  default     = false
}
variable "bi_vpn_access" {
  description = "NOTE: This is mutually exclusive with public BI access, both cannot be enabled at the same time. If BI is enabled we can create an OpenVPN connection to the BI database for secure internet access to the server."
  type        = bool
  default     = false
}
variable "bi_access_cidrs" {
  description = "If BI and public access is enabled, these CIDRs will be permitted through the firewall to access it. If VPN is enabled, these are the CIDRs that are allowed to connect to the VPN server."
  type        = list(string)
  default     = ["127.0.0.1/32"]
}
variable "app_public_access" {
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

variable "eks_kms_key_id" {
  description = "AWS KMS key identifier for EKS encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN"
  type        = string
  default     = ""
}
variable "vpc_id" {
  description = "The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private"
  type        = string
  default     = ""
}
variable "eks_instance_types" {
  description = "The instance type of each node in the application's EKS worker node group."
  default     = ["m5.large", "m5a.large", "m5d.large", "m5ad.large"]
  type        = list(string)
}

variable "eks_volume_size" {
  description = "The amount of local storage (in gigabytes) to allocate to each kubernetes node. Keep in mind you will be billed for this amount of storage multiplied by how many nodes you spin up (i.e. 50GB * 4 nodes = 200GB on your bill). For production installations 50GB should be the minimum. This local storage is used as a temporary holding area for uploaded and in-process assets like videos and images."
  default     = 50
  type        = number

  validation {
    condition     = var.eks_volume_size >= 20
    error_message = "Less than 20GB can cause problems even on testing instances."
  }
}

variable "eks_min_size" {
  description = "The minimum amount of nodes we will autoscale to."
  type        = number
  default     = "2"

  validation {
    condition     = var.eks_min_size >= 1
    error_message = "NodeAutoScalingGroupMinSize must be an integer >= 1."
  }
}

variable "eks_max_size" {
  description = "The maximum amount of nodes we will autoscale to."
  type        = number
  default     = "10"

  validation {
    condition     = var.eks_max_size >= 1
    error_message = "NodeAutoScalingGroupMaxSize must be an integer >= 1\nNodeAutoScalingGroupMaxSize must be >= NodeAutoScalingGroupDesiredCapacity & NodeAutoScalingGroupMinSize."
  }
}

variable "eks_desired_capacity" {
  description = "This is what the node count will start out as."
  type        = number
  default     = "3"

  validation {
    condition     = var.eks_desired_capacity >= 1
    error_message = "NodeAutoScalingGroupDesiredCapacity must be an integer >= 1\nNodeAutoScalingGroupDesiredCapacity must be >= NodeAutoScalingGroupMinSize\nNodeAutoScalingGroupDesiredCapacity must be <= NodeAutoScalingGroupMaxSize."
  }
}
variable "replicated_ui_access_cidr" {
  description = "This CIDR will be allowed to connect to the app dashboard. This is where you upgrade to new versions as well as view cluster status and start/stop the cluster. You probably want to lock this down to your company network CIDR, especially if you chose 'true' for public access."
  type        = string
  default     = "0.0.0.0/0"
}

variable "app_access_cidr" {
  description = "This CIDR will be allowed to connect to Dozuki. If running a public site, use the default value. Otherwise you probably want to lock this down to the VPC or your VPN CIDR."
  type        = string
  default     = "0.0.0.0/0"
}
variable "enable_webhooks" {
  description = "This option will spin up a managed Kafka & Redis cluster to support private webhooks."
  type        = bool
  default     = false
}
variable "aws_profile" {
  description = "If running terraform from a workstation, which AWS CLI profile should we use for asset provisioning."
  type        = string
  default     = ""
}