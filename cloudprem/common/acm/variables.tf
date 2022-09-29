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
variable "namespace" {
  description = "For ACM and SSM parameter paths, this is part of the path to ensure uniqueness with multiple invocations"
  default     = "general"
  type        = string
}
variable "ca_common_name" {
  description = "common name for SSL certificate authority"
  type        = string
  default     = ""
}
variable "cert_common_name" {
  description = "common name for SSL certificate"
  type        = string
  default     = ""
}