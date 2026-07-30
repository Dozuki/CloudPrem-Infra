terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
    tls = { source = "hashicorp/tls" }
  }
}


locals {
  identifier = var.identifier == "" ? "dozuki-${var.environment}" : "${var.identifier}-dozuki-${var.environment}"

  ssm_prefix = "/dozuki/${coalesce(var.identifier, "general")}/${var.environment}/${data.aws_region.current.region}"

  # Tags for all resources. If you add a tag, it must never be blank.
  tags = {
    Terraform = "true"
    # Matches the physical root: Service names the workload; Project (constant) and Identifier
    # (duplicate of Customer) are dropped.
    Service     = "mpc"
    Environment = var.environment
  }
}

data "aws_region" "current" {}
data "aws_partition" "current" {}
data "aws_caller_identity" "current" {}