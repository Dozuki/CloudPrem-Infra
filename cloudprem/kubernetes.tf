data "kubernetes_secret" "frontegg" {
  depends_on = [module.replicated]
  count = var.enable_webhooks ? 1 : 0
  metadata {
    name = "frontegg-credentials"
    namespace = "default"
  }
}
module "container_insights" {
  source = "./modules/container-insights"

  depends_on = [module.eks_cluster]

  cluster_name = module.eks_cluster.cluster_id

  region_name = data.aws_region.current.name
}

module "replicated" {
  source = "./modules/replicated"

  depends_on = [module.eks_cluster]

  dozuki_license_parameter_name = local.dozuki_license_parameter_name
  nlb_hostname = module.nlb.this_lb_dns_name
  release_sequence = var.replicated_app_sequence_number
}

resource "kubernetes_config_map" "dozuki_resources" {

  depends_on = [module.eks_cluster]

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
              "hostname": "${module.memcached.cluster_address}",
              "port": ${module.memcached.port}
            }
          ]
        },
        "globalCluster": {
          "servers": [
            {
              "hostname": "${module.memcached.cluster_address}",
              "port": ${module.memcached.port}
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
          "guide-images": "${local.guide_images_bucket}",
          "guide-pdfs": "${local.guide_pdfs_bucket}",
          "documents": "${local.documents_bucket}",
          "guide-objects": "${local.guide_objects_bucket}"
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
          "hostname": "${module.primary_database.this_db_instance_address}",
          "user": "${module.primary_database.this_db_instance_username}",
          "password": "${random_password.primary_database.result}",
          "CAFile": "/etc/dozuki/rds-ca.pem"
        },
        "master": {
          "hostname": "${module.primary_database.this_db_instance_address}",
          "user": "${module.primary_database.this_db_instance_username}",
          "password": "${random_password.primary_database.result}",
          "CAFile": "/etc/dozuki/rds-ca.pem"
        },
        "slave": {
          "hostname": "${module.primary_database.this_db_instance_address}",
          "user": "${module.primary_database.this_db_instance_username}",
          "password": "${random_password.primary_database.result}",
          "CAFile": "/etc/dozuki/rds-ca.pem"
        },
        "sphinx": {
          "hostname": "${module.primary_database.this_db_instance_address}",
          "user": "${module.primary_database.this_db_instance_username}",
          "password": "${random_password.primary_database.result}",
          "CAFile": "/etc/dozuki/rds-ca.pem"
        }
      }
    EOF

    "frontegg.json" = <<-EOF
      {
        "clientId": "${var.frontegg_client_id}",
        "apiToken": "${var.frontegg_api_key}",
        "apiBaseUrl": "http://frontegg-api-gateway.default.svc.cluster.local",
        "authUrl": "https://api.frontegg.com/auth/vendor"
      }
    EOF

    "rds-ca.pem" = file(local.is_us_gov ? "vendor/rds-ca-${data.aws_region.current.name}-2017-root.pem" : "vendor/rds-ca-2019-root.pem")
  }

}

resource "helm_release" "kubed" {

  depends_on = [module.eks_cluster]

  name       = "kubed"
  repository = "https://charts.appscode.com/stable/"
  chart      = "kubed"
  version    = "v0.12.0"

  namespace = "default"
}