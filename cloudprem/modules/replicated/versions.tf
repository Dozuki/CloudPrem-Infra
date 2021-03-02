terraform {
  required_version = ">= 0.14.0"

  required_providers {
    aws        = ">= 3.2.0"
    kubernetes = "~> 1.13.3"
    helm       = "~> 2.0.1"
  }
}