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
variable "kms_key_id" {
  description = "AWS KMS key identifier for EKS encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN"
  type        = string
  default     = "alias/aws/s3"
}
variable "vpc_id" {
  description = "The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private"
  type        = string
  default     = ""
}
variable "eks_instance_type" {
  description = "The instance type of each node in the application's EKS worker node group."
  default     = "t3.medium"
  type        = string
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
  default     = "4"

  validation {
    condition     = var.eks_min_size >= 1
    error_message = "NodeAutoScalingGroupMinSize must be an integer >= 1."
  }
}

variable "eks_max_size" {
  description = "The maximum amount of nodes we will autoscale to."
  type        = number
  default     = "4"

  validation {
    condition     = var.eks_max_size >= 1
    error_message = "NodeAutoScalingGroupMaxSize must be an integer >= 1\nNodeAutoScalingGroupMaxSize must be >= NodeAutoScalingGroupDesiredCapacity & NodeAutoScalingGroupMinSize."
  }
}

variable "eks_desired_capacity" {
  description = "This is what the node count will start out as."
  type        = number
  default     = "4"

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

variable "public_access" {
  description = "Should the app and dashboard be accessible via a publicly routable IP and domain?"
  type        = bool
  default     = true
}