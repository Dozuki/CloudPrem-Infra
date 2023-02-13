resource "kubernetes_namespace" "kots_app" {
  metadata {
    name = local.k8s_namespace_name
  }
}

resource "kubernetes_role" "dozuki_list_role" {

  metadata {
    name      = "dozuki_list_role"
    namespace = local.k8s_namespace
  }

  rule {
    api_groups = ["apps"]
    resources  = ["deployments"]
    verbs      = ["get", "list", "watch"]
  }
}

resource "kubernetes_role_binding" "dozuki_list_role_binding" {

  metadata {
    name      = "dozuki_list_role_binding"
    namespace = local.k8s_namespace
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role.dozuki_list_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = local.k8s_namespace
  }
}

resource "kubernetes_config_map" "dozuki_resources" {

  metadata {
    name      = "dozuki-resources-configmap"
    namespace = local.k8s_namespace
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
        },
        "testCluster": {
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
        "clientId": "${local.frontegg_clientid}",
        "apiToken": "${local.frontegg_apikey}",
        "apiBaseUrl": "http://frontegg-api-gateway.${local.k8s_namespace}.svc.cluster.local",
        "authUrl": "https://api.frontegg.com/auth/vendor"
      }
    EOF

    "rds-ca.pem" = file(local.ca_cert_pem_file)

    "google-translate.json" = <<-EOF
      {
        "apiToken": "${var.google_translate_api_token}"
      }
    EOF

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

    "saml.json" = <<-EOF
      {
        "activeX509CertPath": "/var/www/key/onprem.crt",
        "activePrivateKeyPath": "/var/www/key/onprem.key",
        "pendingX509CertPath": "/var/www/key/onprem.crt",
        "signatureAlgorithm": "RSA_SHA1"
      }
    EOF

  }
}

resource "helm_release" "container_insights" {
  name  = "container-insights"
  chart = "${path.module}/charts/container_insights"

  namespace = local.k8s_namespace

  set {
    name  = "cluster_name"
    value = var.eks_cluster_id
  }

  set {
    name  = "region_name"
    value = data.aws_region.current.name
  }
}