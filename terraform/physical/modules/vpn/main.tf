terraform {
  required_version = ">= 1.2.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "4.57.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "4.0.4"
    }
  }
}


locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-dozuki-${var.environment}"

  ssm_prefix = "/dozuki/${coalesce(var.identifier, "general")}/${var.environment}/${data.aws_region.current.name}"

  # Tags for all resources. If you add a tag, it must never be blank.
  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Identifier  = coalesce(var.identifier, "NA")
    Environment = var.environment
  }
}

data "aws_region" "current" {}
data "aws_partition" "current" {}
data "aws_caller_identity" "current" {}