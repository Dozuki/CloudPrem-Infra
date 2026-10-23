# Optional managed OpenSearch domain for the application search backend. Off unless
# search_backend = "managed" or opensearch_domain_enabled = true. The domain sits in the
# layer's VPC, takes ingress only from the EKS cluster primary SG (the SG every Auto Mode
# node ENI carries), and authenticates with a fine-grained-access-control internal master
# user whose password lives in Secrets Manager.

locals {
  opensearch_domain_enabled = coalesce(var.opensearch_domain_enabled, var.search_backend == "managed")
  # Zone awareness needs 2+ nodes; AZs used = min(nodes, distinct AZs with a private subnet, 3).
  # Capping by the real AZ list (not a constant) lets an adopted 2-AZ VPC run 4 or 6 nodes.
  # max(1, ...) keeps the modulo below defined when no subnets exist; the shortfall
  # precondition then reports it.
  opensearch_az_count = var.opensearch_instance_count >= 2 ? max(1, min(var.opensearch_instance_count, length(local.opensearch_subnet_candidates), 3)) : 1
  # Domain names must match ^[a-z][0-9a-z-]{2,27}$. The "os-" prefix fixes identifiers that
  # start with a digit; a long identifier keeps 18 chars plus a short hash so names stay unique.
  opensearch_domain_name = (
    length("os-${local.identifier}") <= 28
    ? lower("os-${local.identifier}")
    : "os-${substr(lower(local.identifier), 0, 18)}-${substr(sha1(local.identifier), 0, 6)}"
  )
}

# The domain needs exactly one subnet per AZ. module.vpc hands back one private subnet per
# AZ in AZ order, but a pre-existing VPC (data.aws_subnets) has no such guarantee, so look
# the subnets up and keep the first per AZ (AZs sorted, subnet ids sorted within an AZ).
# Changing subnet_ids is an in-place (blue/green) domain update in the AWS provider; only
# adding or removing vpc_options forces a new domain. An adopted VPC that gains a subnet in
# an earlier-sorting AZ will trigger a blue/green update.
data "aws_subnet" "opensearch" {
  count = local.opensearch_domain_enabled && !local.create_vpc ? length(local.private_subnet_ids) : 0
  id    = local.private_subnet_ids[count.index]
}

locals {
  opensearch_subnet_by_az = local.opensearch_domain_enabled && !local.create_vpc ? {
    for s in data.aws_subnet.opensearch : s.availability_zone => s.id...
  } : {}
  opensearch_subnet_candidates = local.create_vpc ? local.private_subnet_ids : [for az in sort(keys(local.opensearch_subnet_by_az)) : sort(local.opensearch_subnet_by_az[az])[0]]
  # Capped at what exists so slice() cannot fail on an adopted VPC with fewer AZs; the
  # domain precondition below reports the shortfall.
  opensearch_subnet_ids = slice(
    local.opensearch_subnet_candidates,
    0,
    local.opensearch_domain_enabled ? min(local.opensearch_az_count, length(local.opensearch_subnet_candidates)) : 0
  )
}

resource "random_password" "opensearch" {
  count = local.opensearch_domain_enabled ? 1 : 0

  length           = 32
  min_upper        = 1
  min_lower        = 1
  min_numeric      = 1
  min_special      = 1
  override_special = "!#$%&*()-_=+[]{}<>:?"
}

resource "aws_secretsmanager_secret" "opensearch" {
  count = local.opensearch_domain_enabled ? 1 : 0

  name_prefix             = "${local.identifier}-opensearch-"
  recovery_window_in_days = var.protect_resources ? 30 : 0
  tags                    = local.tags
}

resource "aws_secretsmanager_secret_version" "opensearch" {
  count = local.opensearch_domain_enabled ? 1 : 0

  secret_id = aws_secretsmanager_secret.opensearch[0].id
  secret_string = jsonencode({
    username = "master"
    password = random_password.opensearch[0].result
  })
}

resource "aws_security_group" "opensearch" {
  count = local.opensearch_domain_enabled ? 1 : 0

  name_prefix = "${local.identifier}-opensearch-"
  description = "Managed OpenSearch domain, HTTPS from the EKS cluster primary SG only"
  vpc_id      = local.vpc_id
  tags        = local.tags

  ingress {
    description     = "HTTPS from EKS nodes"
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    security_groups = [module.eks_cluster.cluster_primary_security_group_id]
  }

  lifecycle {
    create_before_destroy = true
  }
}

# The OpenSearch service-linked role is account-wide, so it is created out of band (like
# the DMS roles in bi.tf) to keep it out of this stack's state. The script is idempotent and
# the count only follows the enable flag, so the plan does not change once the role exists.
resource "null_resource" "opensearch_slr" {
  count = local.opensearch_domain_enabled ? 1 : 0

  provisioner "local-exec" {
    # Same credentials as the provider: aws_profile is empty on Spacelift and set on a local run.
    # Spacelift workers have no default region; without AWS_REGION the CLI hits the commercial
    # IAM endpoint and fails in aws-us-gov.
    environment = merge(
      { AWS_REGION = data.aws_region.current.region },
      var.aws_profile != "" ? { AWS_PROFILE = var.aws_profile } : {}
    )
    # Accept the current or the legacy (Elasticsearch) role name. Otherwise create it and sleep
    # for IAM propagation before the domain create. If the create fails because a concurrent apply
    # made the role first, re-check and succeed when it now exists.
    command = <<-EOT
      set -e
      if aws iam get-role --role-name AWSServiceRoleForAmazonOpenSearchService >/dev/null 2>&1 \
        || aws iam get-role --role-name AWSServiceRoleForAmazonElasticsearchService >/dev/null 2>&1; then
        exit 0
      fi
      if ! aws iam create-service-linked-role --aws-service-name opensearchservice.${data.aws_partition.current.dns_suffix}; then
        # Lost a race with a concurrent first apply: wait for that run's role to become visible.
        for i in 1 2 3 4 5 6; do
          aws iam get-role --role-name AWSServiceRoleForAmazonOpenSearchService >/dev/null 2>&1 && break
          if [ "$i" = 6 ]; then
            echo "OpenSearch service-linked role still missing after create failed" >&2
            exit 1
          fi
          sleep 5
        done
      fi
      sleep 15
    EOT
  }
}

resource "aws_opensearch_domain" "this" {
  count = local.opensearch_domain_enabled ? 1 : 0

  domain_name    = local.opensearch_domain_name
  engine_version = var.opensearch_engine_version

  cluster_config {
    instance_type          = var.opensearch_instance_type
    instance_count         = var.opensearch_instance_count
    zone_awareness_enabled = local.opensearch_az_count > 1

    dynamic "zone_awareness_config" {
      for_each = local.opensearch_az_count > 1 ? [1] : []
      content {
        availability_zone_count = local.opensearch_az_count
      }
    }
  }

  ebs_options {
    ebs_enabled = true
    volume_type = "gp3"
    volume_size = var.opensearch_ebs_size
  }

  # Service default key on purpose: no KMS key resource to own or leak.
  encrypt_at_rest {
    enabled = true
  }

  node_to_node_encryption {
    enabled = true
  }

  domain_endpoint_options {
    enforce_https       = true
    tls_security_policy = "Policy-Min-TLS-1-2-PFS-2023-10"
  }

  advanced_security_options {
    enabled                        = true
    internal_user_database_enabled = true

    master_user_options {
      master_user_name     = "master"
      master_user_password = random_password.opensearch[0].result
    }
  }

  vpc_options {
    subnet_ids         = local.opensearch_subnet_ids
    security_group_ids = [aws_security_group.opensearch[0].id]
  }

  tags = local.tags

  lifecycle {
    precondition {
      condition     = can(regex("^[a-z][0-9a-z-]{2,27}$", local.opensearch_domain_name))
      error_message = "The derived OpenSearch domain name '${local.opensearch_domain_name}' must match ^[a-z][0-9a-z-]{2,27}$ (lowercase letters, digits and hyphens only)."
    }
    precondition {
      condition     = length(local.opensearch_subnet_ids) == local.opensearch_az_count
      error_message = "The VPC has private subnets in ${length(local.opensearch_subnet_candidates)} distinct AZ(s) but the domain needs ${local.opensearch_az_count}. Add private subnets or lower opensearch_instance_count."
    }
    precondition {
      condition     = var.opensearch_instance_count % local.opensearch_az_count == 0
      error_message = "opensearch_instance_count (${var.opensearch_instance_count}) must be a multiple of the AZ count (${local.opensearch_az_count}) the domain spreads across (the lower of 3 and the number of AZs with a private subnet)."
    }
  }

  depends_on = [null_resource.opensearch_slr]
}

# Separate resource: the policy needs the domain's own ARN, and referencing it from inside
# the domain would be a cycle. Network access is the SG above; FGAC authenticates callers.
resource "aws_opensearch_domain_policy" "this" {
  count = local.opensearch_domain_enabled ? 1 : 0

  domain_name = aws_opensearch_domain.this[0].domain_name
  access_policies = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = "*" }
      Action    = "es:*"
      Resource  = [aws_opensearch_domain.this[0].arn, "${aws_opensearch_domain.this[0].arn}/*"]
    }]
  })
}
