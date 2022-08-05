
resource "aws_security_group" "kafka" {
  count = var.enable_webhooks ? 1 : 0

  name        = "${local.identifier}-webhooks"
  description = "Webhooks SG. Allows access to the kafka cluster"
  vpc_id      = local.vpc_id

  lifecycle {
    create_before_destroy = true
  }

  tags = merge(
    {
      "Name" = format("%s", "${local.identifier}-webhooks")
    },
    local.tags
  )
}

resource "aws_security_group_rule" "kafka_egress" {
  count = var.enable_webhooks ? 1 : 0

  description       = "Allow all egress traffic"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"] #tfsec:ignore:AWS007
  security_group_id = join("", aws_security_group.kafka.*.id)
  type              = "egress"
}

resource "aws_security_group_rule" "kafka_ingress_security_groups" {
  count = var.enable_webhooks ? 1 : 0

  description              = "Allow inbound traffic from existing Security Groups"
  from_port                = 0
  to_port                  = 0
  protocol                 = "-1"
  source_security_group_id = module.eks_cluster.cluster_primary_security_group_id
  security_group_id        = join("", aws_security_group.kafka.*.id)
  type                     = "ingress"
}

resource "aws_security_group_rule" "kafka_ingress_cidr_blocks" {
  count = var.enable_webhooks ? 1 : 0

  description       = "Allow inbound traffic from CIDR blocks"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = [local.vpc_cidr]
  security_group_id = join("", aws_security_group.kafka.*.id)
  type              = "ingress"
}

resource "aws_msk_configuration" "this" {
  count = var.enable_webhooks ? 1 : 0

  kafka_versions = ["2.7.0"]
  name           = "${local.identifier}-kafka-config"

  server_properties = <<PROPERTIES
auto.create.topics.enable = true
delete.topic.enable = true
PROPERTIES
}

resource "aws_msk_cluster" "this" {
  count = var.enable_webhooks ? 1 : 0

  cluster_name           = "${local.identifier}-kafka"
  kafka_version          = "2.7.0"
  number_of_broker_nodes = var.azs_count

  broker_node_group_info {
    instance_type = "kafka.t3.small"
    storage_info {
      ebs_storage_info {
        volume_size = 50
      }
    }
    client_subnets  = local.private_subnet_ids
    security_groups = [join("", aws_security_group.kafka.*.id)]
  }

  configuration_info {
    arn      = aws_msk_configuration.this[0].arn
    revision = aws_msk_configuration.this[0].latest_revision
  }
  encryption_info {
    encryption_in_transit {
      client_broker = "TLS_PLAINTEXT"
    }
  }

  open_monitoring {
    prometheus {
      jmx_exporter {
        enabled_in_broker = true
      }
      node_exporter {
        enabled_in_broker = true
      }
    }
  }

  tags = local.tags
}