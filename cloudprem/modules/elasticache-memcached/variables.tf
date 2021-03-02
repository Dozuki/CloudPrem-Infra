variable "name" {
  type        = string
  description = "Name of the application"
}

variable "vpc_id" {
  type        = string
  default     = ""
  description = "VPC ID"
}

variable "subnet_ids" {
  type        = list(string)
  default     = []
  description = "AWS subnet ids"
}

variable "create_security_group" {
  type        = bool
  description = "Flag to enable/disable creation of Security Group in the module."
  default     = true
}

variable "existing_security_groups" {
  type        = list(string)
  default     = []
  description = "List of existing Security Group IDs to place the cluster into. Set `use_existing_security_groups` to `true` to enable using `existing_security_groups` as Security Groups for the cluster"
}

variable "allowed_security_groups" {
  type        = list(string)
  default     = []
  description = "List of Security Group IDs that are allowed ingress to the cluster's Security Group created in the module"
}

variable "allowed_cidr_blocks" {
  type        = list(string)
  default     = []
  description = "List of CIDR blocks that are allowed ingress to the cluster's Security Group created in the module"
}

variable "engine_version" {
  type        = string
  default     = "1.5.16"
  description = "Memcached engine version. For more info, see https://docs.aws.amazon.com/AmazonElastiCache/latest/mem-ug/supported-engine-versions.html"
}

variable "port" {
  type        = number
  default     = 11211
  description = "Memcached port"
}

variable "instance_type" {
  type        = string
  default     = "cache.t2.micro"
  description = "Elastic cache instance type"
}

variable "cluster_size" {
  type        = number
  default     = 1
  description = "Cluster size"
}

variable "elasticache_subnet_group_name" {
  type        = string
  description = "Subnet group name for the ElastiCache instance"
  default     = ""
}

variable "elasticache_parameter_group_family" {
  type        = string
  description = "ElastiCache parameter group family"
  default     = "memcached1.5"
}

variable "tags" {
  type        = map(string)
  description = "Additional tags (_e.g._ map(\"BusinessUnit\",\"ABC\")"
  default     = {}
}