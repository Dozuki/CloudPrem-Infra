resource "aws_security_group" "this" {
  count = var.create_security_group ? 1 : 0

  name        = "${var.name}-elasticache"
  description = "Elasticache memcached SG. Allows access on the ${var.port} port"
  vpc_id      = var.vpc_id

  lifecycle {
    create_before_destroy = true
  }

  tags = merge(
    {
      "Name" = format("%s", "${var.name}-elasticache")
    },
    var.tags
  )
}

resource "aws_security_group_rule" "egress" {
  count             = var.create_security_group ? 1 : 0
  description       = "Allow all egress traffic"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"] #tfsec:ignore:AWS007
  security_group_id = join("", aws_security_group.this.*.id)
  type              = "egress"
}

resource "aws_security_group_rule" "ingress_security_groups" {
  count                    = var.create_security_group ? length(var.allowed_security_groups) : 0
  description              = "Allow inbound traffic from existing Security Groups"
  from_port                = var.port
  to_port                  = var.port
  protocol                 = "tcp"
  source_security_group_id = var.allowed_security_groups[count.index]
  security_group_id        = join("", aws_security_group.this.*.id)
  type                     = "ingress"
}

resource "aws_security_group_rule" "ingress_cidr_blocks" {
  count             = var.create_security_group ? 1 : 0
  description       = "Allow inbound traffic from CIDR blocks"
  from_port         = var.port
  to_port           = var.port
  protocol          = "tcp"
  cidr_blocks       = var.allowed_cidr_blocks
  security_group_id = join("", aws_security_group.this.*.id)
  type              = "ingress"
}

resource "null_resource" "cluster_urls" {
  count = var.cluster_size

  triggers = {
    name = "${replace(
      aws_elasticache_cluster.this.cluster_address,
      ".cfg.",
      format(".%04d.", count.index + 1)
    )}:${var.port}"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_elasticache_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
}

resource "aws_elasticache_parameter_group" "this" {
  name   = var.name
  family = var.elasticache_parameter_group_family
}

resource "aws_elasticache_cluster" "this" {
  cluster_id = var.name

  engine         = "memcached"
  engine_version = var.engine_version
  port           = var.port

  node_type       = var.instance_type
  num_cache_nodes = var.cluster_size

  az_mode            = var.cluster_size == 1 ? "single-az" : "cross-az"
  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = concat(var.create_security_group ? [aws_security_group.this[0].id] : [], var.existing_security_groups)

  parameter_group_name = aws_elasticache_parameter_group.this.name

  apply_immediately = true

  tags = var.tags
}