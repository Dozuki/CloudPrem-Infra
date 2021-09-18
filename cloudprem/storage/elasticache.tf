resource "aws_security_group" "elasticache" {

  name        = "${local.identifier}-elasticache"
  description = "Elasticache memcached SG. Allows access on the 11211 port"
  vpc_id      = var.vpc_id

  lifecycle {
    create_before_destroy = true
  }

  tags = merge(
    {
      "Name" = format("%s", "${local.identifier}-elasticache")
    },
    local.tags
  )
}

resource "aws_security_group_rule" "egress" {
  description       = "Allow all egress traffic"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"] #tfsec:ignore:AWS007
  security_group_id = aws_security_group.elasticache.id
  type              = "egress"
}

resource "aws_security_group_rule" "ingress_cidr_blocks" {
  description       = "Allow inbound traffic from CIDR blocks"
  from_port         = 11211
  to_port           = 11211
  protocol          = "tcp"
  cidr_blocks       = [data.aws_vpc.main.cidr_block]
  security_group_id = aws_security_group.elasticache.id
  type              = "ingress"
}

resource "null_resource" "cluster_urls" {
  count = var.elasticache_cluster_size

  triggers = {
    name = "${replace(
      aws_elasticache_cluster.this.cluster_address,
      ".cfg.",
      format(".%04d.", count.index + 1)
    )}:11211"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_elasticache_subnet_group" "this" {
  name       = local.identifier
  subnet_ids = data.aws_subnets.private.ids
}

resource "aws_elasticache_parameter_group" "this" {
  name   = local.identifier
  family = "memcached1.5"
}

resource "aws_elasticache_cluster" "this" {
  cluster_id = local.identifier

  engine         = "memcached"
  engine_version = "1.5.16"
  port           = 11211

  node_type       = var.elasticache_instance_type
  num_cache_nodes = var.elasticache_cluster_size

  az_mode            = var.elasticache_cluster_size == 1 ? "single-az" : "cross-az"
  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.elasticache.id]

  parameter_group_name = aws_elasticache_parameter_group.this.name

  apply_immediately = true

  tags = local.tags
}
