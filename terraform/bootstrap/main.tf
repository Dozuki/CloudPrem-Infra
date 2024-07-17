terraform {
  required_version = ">= 1.3.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.58.0"
    }
  }
}

locals {
  # Tags for all resources. If you add a tag, it must never be blank.
  tags = {
    Terraform   = "true"
    Project     = "Dozuki"
    Environment = "bootstrap"
  }
}

resource "aws_ssm_parameter" "customer_ids" {
  for_each = var.customer_id_parameters

  name        = "/dozuki/workstation/kots/${each.key}/customer_id"
  description = "Customer ID for ${each.key} deployments"
  type        = "SecureString"
  value       = each.value

  tags = local.tags
}


