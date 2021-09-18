
locals {
  frontegg_api_key   = var.enable_webhooks ? try(data.kubernetes_secret.frontegg[0].data.apikey, "") : ""
  frontegg_client_id = var.enable_webhooks ? try(data.kubernetes_secret.frontegg[0].data.clientid, "") : ""
}

data "kubernetes_secret" "frontegg" {
  depends_on = [helm_release.replicated]
  count      = var.enable_webhooks ? 1 : 0
  metadata {
    name      = "frontegg-credentials"
    namespace = "default"
  }
}

data "aws_secretsmanager_secret_version" "db_master" {
  secret_id = var.primary_db_secret
}

resource "random_password" "dashboard_password" {
  length  = 16
  special = true
}

resource "kubernetes_config_map" "dozuki_resources" {

  metadata {
    name      = "dozuki-resources-configmap"
    namespace = "default"

    annotations = {
      "kubed.appscode.com/sync" = ""
    }
  }

  data = {
    "memcached.json" = <<-EOF
      {
        "localCluster": {
          "servers": [
            {
              "hostname": "${var.memcached_cluster_address}",
              "port": 11211
            }
          ]
        },
        "globalCluster": {
          "servers": [
            {
              "hostname": "${var.memcached_cluster_address}",
              "port": 11211
            }
          ]
        }
      }
    EOF

    "aws-resources.json" = <<-EOF
      {
        "S3.enabled": true,
        "Ec2.enabled": true,
        "CloudFront.enabled": false,
        "LH.localFileSystem": false,
        "CdnUrls.alwaysRelative": false
      }
    EOF

    "s3.json" = <<-EOF
      {
        "region": "${data.aws_region.current.name}",
        "encryptionKeyId": "${data.aws_kms_key.s3.arn}"
      }
    EOF

    "buckets.json" = <<-EOF
      {
        "default": {
          "guide-images": "${var.s3_images_bucket}",
          "guide-pdfs": "${var.s3_pdfs_bucket}",
          "documents": "${var.s3_documents_bucket}",
          "guide-objects": "${var.s3_objects_bucket}"
        }
      }
    EOF

    "sentry.json" = <<-EOF
      {
        "tags": {
          "deployment": "CloudPrem",
        }
      }
    EOF

    "db.json" = <<-EOF
      {
        "generic": {
          "hostname": "${local.db_master_host}",
          "user": "${local.db_master_username}",
          "password": "${local.db_master_password}",
          "CAFile": "/etc/dozuki/rds-ca.pem"
        },
        "master": {
          "hostname": "${local.db_master_host}",
          "user": "${local.db_master_username}",
          "password": "${local.db_master_password}",
          "CAFile": "/etc/dozuki/rds-ca.pem"
        },
        "slave": {
          "hostname": "${local.db_master_host}",
          "user": "${local.db_master_username}",
          "password": "${local.db_master_password}",
          "CAFile": "/etc/dozuki/rds-ca.pem"
        },
        "sphinx": {
          "hostname": "${local.db_master_host}",
          "user": "${local.db_master_username}",
          "password": "${local.db_master_password}",
          "CAFile": "/etc/dozuki/rds-ca.pem"
        }
      }
    EOF

    "frontegg.json" = <<-EOF
      {
        "clientId": "${local.frontegg_client_id}",
        "apiToken": "${local.frontegg_api_key}",
        "apiBaseUrl": "http://frontegg-api-gateway.default.svc.cluster.local",
        "authUrl": "https://api.frontegg.com/auth/vendor"
      }
    EOF

    "rds-ca.pem" = file(local.is_us_gov ? "vendor/rds-ca-${data.aws_region.current.name}-2017-root.pem" : "vendor/rds-ca-2019-root.pem")

    "index.json" = <<-EOF
       {
         "index": {
           "legacy": {
             "filename": "legacy.json"
           },
           "s3": {
             "filename": "s3.json"
           },
           "buckets": {
             "filename": "buckets.json"
           },
           "db": {
             "filename": "db.json"
           },
           "memcached": {
             "filename": "memcached.json"
           },
           "aws-resources": {
             "filename": "aws-resources.json"
           }
         }
       }
     EOF
  }

  lifecycle {
    ignore_changes = [metadata]
  }
}

resource "helm_release" "kubed" {

  name    = "kubed"
  chart   = "${path.module}/charts/kubed"
  version = "v0.12.0"

  namespace = "default"
}