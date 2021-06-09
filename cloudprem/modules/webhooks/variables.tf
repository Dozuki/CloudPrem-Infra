variable "vpc_id" {
  description = "The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private"
  type        = string
  default     = ""
}
variable "subnet_ids" {
  type        = list(string)
  default     = []
  description = "AWS subnet ids"
}
variable "name" {
  type = string
  default = ""
  description = "Name of the MSK cluster"
}
variable "rds_kms_key_id" {
  type = string
  default = ""
  description = "KMS Key ID"
}
variable "kafka_cluster_size" {
  type = number
  default = 2
  description = "The amount of kafka brokers"
}
variable "instance_size" {
  type = string
  default = "kafka.t3.small"
  description = "The instance size of the kafka brokers"
}
variable "volume_size" {
  type = number
  default = 100
  description = "Size in GB for kafka storage volumes"
}
variable "allowed_cidr_blocks" {
  type        = list(string)
  default     = []
  description = "List of CIDR blocks that are allowed ingress to the cluster's Security Group created in the module"
}
variable "tags" {
  type        = map(string)
  description = "Additional tags (_e.g._ map(\"BusinessUnit\",\"ABC\")"
  default     = {}
}
variable "rds_address" {
  type = string
  description = "Primary database hostname"
}
variable "rds_user" {
  type = string
  description = "Primary database username"
}
variable "rds_pass" {
  type = string
  description = "Primary database password"
}
variable "eks_sg" {
  type = string
  description = "EKS cluster security group"
}
variable "frontegg_secret" {
  type = string
  description = "Kubernetes secret with frontegg authentication data created by replicated"
}
variable "eks_cluster" {
  type = string
  description = "Name of the deployed EKS cluster"
}