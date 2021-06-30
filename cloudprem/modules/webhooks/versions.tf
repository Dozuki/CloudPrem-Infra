terraform {
  required_version = "0.14.10"

  required_providers {
    aws  = ">= 3.22.0"
    null = "~> 2.0"
    helm = "~> 2.1.2"
  }
}