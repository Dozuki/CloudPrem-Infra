data "aws_kms_key" "rds" {
  key_id = var.rds_kms_key_id
}
data "kubernetes_secret" "frontegg" {
  metadata {
    name = "frontegg-credentials"
    namespace = "default"
  }
}
//resource "kubernetes_config_map" "mongo_configmap" {
//
//  metadata {
//    name = "mongo-tls"
//    namespace = "default"
//  }
//
//  data = {
//    "rds-combined-ca-bundle.pem" = file("./modules/webhooks/vendor/rds-combined-ca-bundle.pem")
//  }
//}
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

//resource "aws_cloudwatch_log_group" "logs" {
//  name = "msk_broker_logs"
//}
//
//resource "aws_s3_bucket" "bucket" {
//  bucket = "${var.name}-msk-broker-logs-bucket"
//  acl    = "private"
//}


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

//  encryption_info {
//    encryption_at_rest_kms_key_arn = data.aws_kms_key.rds.arn
//  }

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

//  logging_info {
//    broker_logs {
//      cloudwatch_logs {
//        enabled   = true
//        log_group = aws_cloudwatch_log_group.logs.name
//      }
//      s3 {
//        enabled = true
//        bucket  = aws_s3_bucket.bucket.id
//        prefix  = "logs/msk-"
//      }
//    }
//  }

  tags = var.tags
}
resource "aws_docdb_subnet_group" "default" {
  name       = "${var.name}-mongo-subnet_group"
  subnet_ids = var.subnet_ids

  tags = var.tags
}
resource "aws_docdb_cluster_parameter_group" "disable_tls" {
  family      = "docdb4.0"
  name        = "${var.name}-mongo-param-grop"
  description = "docdb cluster parameter group"

  parameter {
    name  = "tls"
    value = "disabled"
  }
}

resource "aws_docdb_cluster_instance" "cluster_instances" {
  count              = 1
  identifier         = "${var.name}-mongo-${count.index}"
  cluster_identifier = aws_docdb_cluster.this.id
  instance_class     = "db.t3.medium"


  tags = var.tags
}

resource "aws_docdb_cluster" "this" {
  cluster_identifier = "${var.name}-mongo"
  vpc_security_group_ids = [aws_security_group.this.id]
  master_username    = "foo"
  master_password    = "barbut8chars"
  db_subnet_group_name = aws_docdb_subnet_group.default.name
  skip_final_snapshot = true
  db_cluster_parameter_group_name = aws_docdb_cluster_parameter_group.disable_tls.name

  tags = var.tags
}

resource "aws_elasticache_subnet_group" "default" {
  name       = "${var.name}-redis-subnet-group"
  subnet_ids = var.subnet_ids
}

resource "aws_elasticache_cluster" "redis" {
  cluster_id           = "${var.name}-redis"
  engine               = "redis"
  node_type            = "cache.t3.micro"
  subnet_group_name = aws_elasticache_subnet_group.default.name
  num_cache_nodes      = 1
  parameter_group_name = "default.redis5.0"
  engine_version       = "5.0.6"
  security_group_ids = [aws_security_group.this.id]
  port                 = 6379
}


resource "helm_release" "frontegg" {

  depends_on = [
    local_file.default_helmignore,
    local_file.api_helmignore,
    local_file.event_helmignore,
    local_file.webhook_helmignore
  ]

  name  = "connectivity"
  chart = "${path.module}/charts/connectivity"

  namespace = "default"

  timeout = 600
  reuse_values = true
//  force_update = true
//  cleanup_on_fail = true

  set_sensitive {
    name = "event-service.frontegg.clientId"
    value = "dummyid"
  }
  set_sensitive {
    name = "event-service.frontegg.apiKey"
    value = "dummyid"
  }

  set {
    name  = "webhook-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this.zookeeper_connect_string,",","\\,")
  }
  set {
    name  = "event-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this.zookeeper_connect_string,",","\\,")
  }
  set {
    name  = "integrations-service.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this.zookeeper_connect_string,",","\\,")
  }
  set {
    name  = "connectors-worker.messageBroker.brokerList"
    value = replace(aws_msk_cluster.this.zookeeper_connect_string,",","\\,")
  }

  set {
    name = "webhook-service.mongo.connectionString"
    value = "mongodb://foo:barbut8chars@${aws_docdb_cluster.this.endpoint}:${aws_docdb_cluster_instance.cluster_instances.0.port}/?retryWrites=false"
  }
  set {
    name = "integrations-service.mongo.connectionString"
    value = "mongodb://foo:barbut8chars@${aws_docdb_cluster.this.endpoint}:${aws_docdb_cluster_instance.cluster_instances.0.port}/?retryWrites=false"
  }

  set {
    name = "webhook-service.redis.host"
    value = aws_elasticache_cluster.redis.cache_nodes.0.address
  }
  set {
    name = "event-service.redis.host"
    value = aws_elasticache_cluster.redis.cache_nodes.0.address
  }
  set {
    name = "connectors-worker.redis.host"
    value = aws_elasticache_cluster.redis.cache_nodes.0.address
  }

  set {
    name = "webhook-service.mysql.host"
    value = var.rds_address
  }
  set {
    name = "webhook-service.mysql.username"
    value = var.rds_user
  }

  set_sensitive {
    name = "webhook-service.mysql.password"
    value = var.rds_pass
  }
  set {
    name = "event-service.database.host"
    value = var.rds_address
  }
  set {
    name = "event-service.database.username"
    value = var.rds_user
  }

  set_sensitive {
    name = "event-service.database.password"
    value = var.rds_pass
  }

  set {
    name = "api-gateway.frontegg.authenticationPublicKey"
    value = data.kubernetes_secret.frontegg.data.pubkey
  }

  set {
    name = "frontegg.images.username"
    value = data.kubernetes_secret.frontegg.data.username
  }
  set_sensitive {
    name = "frontegg.images.password"
    value = data.kubernetes_secret.frontegg.data.password
  }

  set {
    name = "integrations-service.frontegg.slack.encryptionKey"
    value = "dummy"
  }
  set {
    name = "connectors-worker.frontegg.channels"
    value = "slack"
  }
  set {
    name = "connectors-worker.frontegg.emails.provider"
    value = "sendgrid"
  }
  set {
    name = "connectors-worker.frontegg.emails.sendgrid.apiKey"
    value = "dummyval"
  }

}
resource local_file default_helmignore {
  content     = file("./modules/webhooks/helmignore")
  filename = "./modules/webhooks/charts/connectivity/.helmignore"
  file_permission = "0644"
}
resource local_file event_helmignore {
  content     = file("./modules/webhooks/helmignore")
  filename = "./modules/webhooks/charts/connectivity/charts/event-service/.helmignore"
  file_permission = "0644"
}
resource local_file api_helmignore {
  content     = file("./modules/webhooks/helmignore")
  filename = "./modules/webhooks/charts/connectivity/charts/api-gateway/.helmignore"
  file_permission = "0644"
}
resource local_file webhook_helmignore {
  content     = file("./modules/webhooks/helmignore")
  filename = "./modules/webhooks/charts/connectivity/charts/webhook-service/.helmignore"
  file_permission = "0644"
}
resource local_file connectors_helmignore {
  content     = file("./modules/webhooks/helmignore")
  filename = "./modules/webhooks/charts/connectivity/charts/connectors-worker/.helmignore"
  file_permission = "0644"
}
resource local_file integrations_helmignore {
  content     = file("./modules/webhooks/helmignore")
  filename = "./modules/webhooks/charts/connectivity/charts/integrations-service/.helmignore"
  file_permission = "0644"
}