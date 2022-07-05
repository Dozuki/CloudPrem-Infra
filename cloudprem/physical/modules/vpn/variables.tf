variable "identifier" {
  description = "A name identifier to use as prefix for all the resources."
  type        = string

  validation {
    condition     = length(var.identifier) <= 10
    error_message = "The length of the identifier must be less than 11 characters."
  }
}

variable "environment" {
  description = "Environment of the application"
  type        = string

  validation {
    condition     = length(var.environment) <= 5
    error_message = "The length of the Environment must be less than 6 characters."
  }
}
variable "vpn-client-list" {
  description = "List of VPN Users"
  type        = list(string)
}
variable "session_timeout_hours" {
  description = "Session timeout hours"
  type        = number
  default     = 8
}
variable "vpc_id" {
  description = "The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private"
  type        = string
}
variable "vpc_cidr" {
  description = "The CIDR block for the VPC"
  type        = string
  default     = "172.16.0.0/16"
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
variable "client_cidr_block" {
  description = "AWS VPN client cidr block"
  type        = string
  default     = "172.0.0.0/22"
}
variable "subnet_id" {
  description = "Subnet for client vpn network association"
  type        = string
}
variable "allowed_ingress_cidrs" {
  description = "Allowed CIDRs for VPN connections"
  type        = list(string)
}
variable "s3_kms_key_id" {
  description = "AWS KMS key identifier for S3 encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN"
  type        = string
  default     = "alias/aws/s3"
}