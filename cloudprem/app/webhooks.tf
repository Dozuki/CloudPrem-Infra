locals {
  frontegg_clientid = try(data.kubernetes_secret.frontegg[0].data.clientid, "")
  frontegg_apikey   = try(data.kubernetes_secret.frontegg[0].data.apikey, "")
  frontegg_pub_key  = try(data.kubernetes_secret.frontegg[0].data.pubkey, "")
  frontegg_username = try(data.kubernetes_secret.frontegg[0].data.username, "")
  frontegg_password = try(data.kubernetes_secret.frontegg[0].data.password, "")
}


resource "kubernetes_job" "database_update" {
  count = var.enable_webhooks ? 1 : 0

  metadata {
    name = "frontegg-db-update"
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "frontegg-db-update"
          image = "imega/mysql-client"
          command = [
            "mysql",
            "--host=${local.db_master_host}",
            "--user=${local.db_master_username}",
            "--password=${local.db_master_password}",
            "--execute=${file("static/frontegg-db.sql")}"
          ]
        }
        restart_policy = "Never"
      }
    }
    backoff_limit = 1
    completions   = 1
  }
  wait_for_completion = true
}

resource "aws_security_group" "this" {
  count = var.enable_webhooks ? 1 : 0

  name        = "${local.identifier}-webhooks"
  description = "Webhooks SG. Allows access to the kafka cluster"
  vpc_id      = var.vpc_id

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

resource "aws_security_group_rule" "egress" {
  count = var.enable_webhooks ? 1 : 0

  description       = "Allow all egress traffic"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"] #tfsec:ignore:AWS007
  security_group_id = join("", aws_security_group.this.*.id)
  type              = "egress"
}

resource "aws_security_group_rule" "ingress_security_groups" {
  count = var.enable_webhooks ? 1 : 0

  description              = "Allow inbound traffic from existing Security Groups"
  from_port                = 0
  to_port                  = 0
  protocol                 = "-1"
  source_security_group_id = var.cluster_primary_sg
  security_group_id        = join("", aws_security_group.this.*.id)
  type                     = "ingress"
}

resource "aws_security_group_rule" "ingress_cidr_blocks" {
  count = var.enable_webhooks ? 1 : 0

  description       = "Allow inbound traffic from CIDR blocks"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = [data.aws_vpc.main.cidr_block]
  security_group_id = join("", aws_security_group.this.*.id)
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
    instance_type   = "kafka.t3.small"
    ebs_volume_size = 50
    client_subnets  = data.aws_subnets.private.ids
    security_groups = [join("", aws_security_group.this.*.id)]
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

resource "helm_release" "mongodb" {
  count = var.enable_webhooks ? 1 : 0

  name       = "frontegg-documents"
  repository = "https://charts.bitnami.com/bitnami"
  chart      = "mongodb"
  version    = "10.15.2"

  set {
    name  = "auth.enabled"
    value = "false"
  }
}

resource "helm_release" "redis" {
  count = var.enable_webhooks ? 1 : 0

  name       = "frontegg-kvstore"
  repository = "https://charts.bitnami.com/bitnami"
  chart      = "redis"
  version    = "14.1.1"

  set {
    name  = "auth.enabled"
    value = "false"
  }
  set {
    name  = "tls.authClients"
    value = "false"
  }
  set {
    name  = "architecture"
    value = "standalone"
  }
}


resource "helm_release" "frontegg" {
  count = var.enable_webhooks ? 1 : 0

  depends_on = [
    local_file.default_helmignore,
    local_file.api_helmignore,
    local_file.event_helmignore,
    local_file.webhook_helmignore,
    helm_release.mongodb,
    helm_release.redis,
    kubernetes_job.database_update
  ]

  name  = "frontegg"
  chart = "charts/connectivity"

  namespace = "default"

  reuse_values = true

  values = [
    file("static/webhooks_values.yml")
  ]

  // - Frontegg Auth - //
  set_sensitive {
    name  = "event-service.frontegg.clientId"
    value = local.frontegg_clientid
  }
  set_sensitive {
    name  = "event-service.frontegg.apiKey"
    value = local.frontegg_apikey
  }
  set_sensitive {
    name  = "api-gateway.frontegg.authenticationPublicKey"
    value = local.frontegg_pub_key
  }
  set_sensitive {
    name  = "frontegg.images.username"
    value = local.frontegg_username
  }
  set_sensitive {
    name  = "frontegg.images.password"
    value = local.frontegg_password
  }

  // - Kafka - //
  set {
    name  = "webhook-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this[0].bootstrap_brokers, ",", "\\,")
  }
  set {
    name  = "event-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this[0].bootstrap_brokers, ",", "\\,")
  }
  set {
    name  = "integrations-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this[0].bootstrap_brokers, ",", "\\,")
  }
  set {
    name  = "connectors-worker.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this[0].bootstrap_brokers, ",", "\\,")
  }

  // - MySQL - //
  set {
    name  = "webhook-service.mysql.host"
    value = local.db_master_host
  }
  set {
    name  = "event-service.database.host"
    value = local.db_master_host
  }
  set {
    name  = "webhook-service.mysql.username"
    value = local.db_master_username
  }
  set {
    name  = "event-service.database.username"
    value = local.db_master_username
  }
  set_sensitive {
    name  = "webhook-service.mysql.password"
    value = local.db_master_password
  }
  set_sensitive {
    name  = "event-service.database.password"
    value = local.db_master_password
  }

}
# Necessary hackery to prevent generated terraform/terragrunt files from being included by helm and blowing up the deploy.
resource "local_file" "default_helmignore" {
  count = var.enable_webhooks ? 1 : 0

  content         = file("${path.module}/charts/helmignore")
  filename        = "${path.module}/charts/connectivity/.helmignore"
  file_permission = "0644"
}
resource "local_file" "event_helmignore" {
  count = var.enable_webhooks ? 1 : 0

  content         = file("${path.module}/charts/helmignore")
  filename        = "${path.module}/charts/connectivity/charts/event-service/.helmignore"
  file_permission = "0644"
}
resource "local_file" "api_helmignore" {
  count = var.enable_webhooks ? 1 : 0

  content         = file("${path.module}/charts/helmignore")
  filename        = "${path.module}/charts/connectivity/charts/api-gateway/.helmignore"
  file_permission = "0644"
}
resource "local_file" "webhook_helmignore" {
  count = var.enable_webhooks ? 1 : 0

  content         = file("${path.module}/charts/helmignore")
  filename        = "${path.module}/charts/connectivity/charts/webhook-service/.helmignore"
  file_permission = "0644"
}
resource "local_file" "connectors_helmignore" {
  count = var.enable_webhooks ? 1 : 0

  content         = file("${path.module}/charts/helmignore")
  filename        = "${path.module}/charts/connectivity/charts/connectors-worker/.helmignore"
  file_permission = "0644"
}
resource "local_file" "integrations_helmignore" {
  count = var.enable_webhooks ? 1 : 0

  content         = file("${path.module}/charts/helmignore")
  filename        = "${path.module}/charts/connectivity/charts/integrations-service/.helmignore"
  file_permission = "0644"
}