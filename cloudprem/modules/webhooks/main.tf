data "aws_kms_key" "rds" {
  key_id = var.rds_kms_key_id
}

resource "aws_security_group" "this" {
  name        = "${var.name}-webhooks"
  description = "Webhooks SG. Allows access to the kafka cluster"
  vpc_id      = var.vpc_id

  lifecycle {
    create_before_destroy = true
  }

  tags = merge(
  {
    "Name" = format("%s", "${var.name}-webhooks")
  },
  var.tags
  )
}

resource "aws_security_group_rule" "egress" {
  description       = "Allow all egress traffic"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"] #tfsec:ignore:AWS007
  security_group_id = join("", aws_security_group.this.*.id)
  type              = "egress"
}

resource "aws_security_group_rule" "ingress_security_groups" {
  description              = "Allow inbound traffic from existing Security Groups"
  from_port                = 0
  to_port                  = 0
  protocol                 = "-1"
  source_security_group_id = var.eks_sg
  security_group_id        = join("", aws_security_group.this.*.id)
  type                     = "ingress"
}

resource "aws_security_group_rule" "ingress_cidr_blocks" {
  description       = "Allow inbound traffic from CIDR blocks"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = var.allowed_cidr_blocks
  security_group_id = join("", aws_security_group.this.*.id)
  type              = "ingress"
}

resource "aws_msk_configuration" "this" {
  kafka_versions = ["2.7.0"]
  name           = "${var.name}-kafka-config"

  server_properties = <<PROPERTIES
auto.create.topics.enable = true
delete.topic.enable = true
PROPERTIES
}

resource "aws_msk_cluster" "this" {
  cluster_name           = "${var.name}-kafka"
  kafka_version          = "2.7.0"
  number_of_broker_nodes = var.cluster_size

  broker_node_group_info {
    instance_type   = var.instance_size
    ebs_volume_size = var.volume_size
    client_subnets = var.subnet_ids
    security_groups = [join("", aws_security_group.this.*.id)]
  }

  configuration_info {
    arn = aws_msk_configuration.this.arn
    revision = aws_msk_configuration.this.latest_revision
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

  tags = var.tags
}

resource "helm_release" "mongodb" {

  name = "frontegg-documents"
  repository = "https://charts.bitnami.com/bitnami"
  chart = "mongodb"
  version = "10.15.2"

  set {
    name = "auth.enabled"
    value = "false"
  }
}

resource "helm_release" "redis" {

  name = "frontegg-kvstore"
  repository = "https://charts.bitnami.com/bitnami"
  chart = "redis"
  version = "14.1.1"

  set {
    name = "auth.enabled"
    value = "false"
  }
  set {
    name = "tls.authClients"
    value = "false"
  }
  set {
    name = "architecture"
    value = "standalone"
  }
}


resource "helm_release" "frontegg" {

  depends_on = [
    local_file.default_helmignore,
    local_file.api_helmignore,
    local_file.event_helmignore,
    local_file.webhook_helmignore,
    helm_release.mongodb,
    helm_release.redis
  ]

  name  = "frontegg"
  chart = "${path.module}/charts/connectivity"

  namespace = "default"

  reuse_values = true
//  wait       = false

  values = [
    file("${path.module}/values.yaml")
  ]

  // - Frontegg Auth - //
  set_sensitive {
    name = "event-service.frontegg.clientId"
    value = var.frontegg_client_id
  }
  set_sensitive {
    name = "event-service.frontegg.apiKey"
    value = var.frontegg_api_key
  }
  set_sensitive {
    name = "api-gateway.frontegg.authenticationPublicKey"
    value = var.frontegg_secret.data.pubkey
  }

  // - Kafka - //
  set {
    name  = "webhook-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this.bootstrap_brokers,",","\\,")
  }
  set {
    name  = "event-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this.bootstrap_brokers,",","\\,")
  }
  set {
    name  = "integrations-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this.bootstrap_brokers,",","\\,")
  }
  set {
    name  = "connectors-worker.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this.bootstrap_brokers,",","\\,")
  }

  // - MySQL - //
  set {
    name = "webhook-service.mysql.host"
    value = var.rds_address
  }
  set {
    name = "event-service.database.host"
    value = var.rds_address
  }
  set {
    name = "webhook-service.mysql.username"
    value = var.rds_user
  }
  set {
    name = "event-service.database.username"
    value = var.rds_user
  }
  set_sensitive {
    name = "webhook-service.mysql.password"
    value = var.rds_pass
  }
  set_sensitive {
    name = "event-service.database.password"
    value = var.rds_pass
  }

  // - Frontegg Docker Auth - //
  set_sensitive {
    name = "frontegg.images.username"
    value = var.frontegg_secret.data.username
  }
  set_sensitive {
    name = "frontegg.images.password"
    value = var.frontegg_secret.data.password
  }

}
# Necessary hackery to prevent generated terraform/terragrunt files from being included by helm and blowing up the deploy.
resource local_file default_helmignore {
  content     = file("${path.module}/helmignore")
  filename = "${path.module}/charts/connectivity/.helmignore"
  file_permission = "0644"
}
resource local_file event_helmignore {
  content     = file("${path.module}/helmignore")
  filename = ".${path.module}/charts/connectivity/charts/event-service/.helmignore"
  file_permission = "0644"
}
resource local_file api_helmignore {
  content     = file("${path.module}/helmignore")
  filename = "${path.module}/charts/connectivity/charts/api-gateway/.helmignore"
  file_permission = "0644"
}
resource local_file webhook_helmignore {
  content     = file("${path.module}/helmignore")
  filename = "${path.module}/charts/connectivity/charts/webhook-service/.helmignore"
  file_permission = "0644"
}
resource local_file connectors_helmignore {
  content     = file("${path.module}/helmignore")
  filename = "${path.module}/charts/connectivity/charts/connectors-worker/.helmignore"
  file_permission = "0644"
}
resource local_file integrations_helmignore {
  content     = file("${path.module}/helmignore")
  filename = "${path.module}/charts/connectivity/charts/integrations-service/.helmignore"
  file_permission = "0644"
}