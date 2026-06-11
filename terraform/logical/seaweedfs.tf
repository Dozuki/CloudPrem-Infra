# SeaweedFS in-cluster S3-compatible object storage (Azure only).
#
# On AWS the app uses real S3 buckets; on Azure the app talks to a SeaweedFS
# filer with the embedded S3 gateway enabled. The helm release name is
# "seaweedfs" so the chart fullname collapses to "seaweedfs" and the filer S3
# gateway Service is "seaweedfs-s3" on port 8333 (verified against chart
# 4.33.0 rendered manifests).

locals {
  seaweedfs_s3_endpoint = "http://seaweedfs-s3.${local.k8s_namespace_name}.svc.cluster.local:8333"

  seaweedfs_buckets = [
    for b in [
      var.s3_images_bucket,
      var.s3_objects_bucket,
      var.s3_documents_bucket,
      var.s3_pdfs_bucket,
    ] : b if b != ""
  ]
}

resource "random_password" "seaweedfs_access_key" {
  count = var.cloud == "azure" ? 1 : 0

  length  = 20
  special = false
}

resource "random_password" "seaweedfs_secret_key" {
  count = var.cloud == "azure" ? 1 : 0

  length  = 40
  special = false
}

resource "helm_release" "seaweedfs" {
  count = var.cloud == "azure" ? 1 : 0

  name       = "seaweedfs"
  namespace  = kubernetes_namespace_v1.app.metadata[0].name
  repository = "https://seaweedfs.github.io/seaweedfs/helm"
  chart      = "seaweedfs"
  version    = "4.33.0"

  wait    = true
  timeout = 600

  values = [
    yamlencode({
      master = {
        replicas = 1
        # 001 = one replica on a second volume server; a single PVC loss no longer loses data.
        defaultReplication = "001"
        data = {
          type         = "persistentVolumeClaim"
          size         = "10Gi"
          storageClass = "managed-csi"
        }
        # Chart default is hostPath, which we do not want on AKS nodes.
        logs = {
          type = "emptyDir"
        }
      }
      volume = {
        replicas = 2
        dataDirs = [
          {
            name         = "data"
            type         = "persistentVolumeClaim"
            size         = "${var.seaweedfs_volume_size_gb}Gi"
            storageClass = "managed-csi"
            maxVolumes   = 0
          }
        ]
        logs = {
          type = "emptyDir"
        }
      }
      filer = {
        enabled  = true
        replicas = 1
        # 001 = one replica on a second volume server; a single PVC loss no longer loses data.
        defaultReplicaPlacement = "001"
        data = {
          type         = "persistentVolumeClaim"
          size         = "10Gi"
          storageClass = "managed-csi"
        }
        logs = {
          type = "emptyDir"
        }
        # Filer-embedded S3 gateway. enableAuth makes the chart render
        # the seaweedfs-s3-secret with a seaweedfs_s3_config JSON built
        # from s3.credentials below and start the filer with -s3.config.
        s3 = {
          enabled    = true
          enableAuth = true
        }
      }
    })
  ]

  # The chart's s3-secret.yaml only reads credentials from the top-level
  # s3.credentials key, even when the S3 gateway runs embedded in the filer.
  set_sensitive {
    name  = "s3.credentials.admin.accessKey"
    value = random_password.seaweedfs_access_key[0].result
  }
  set_sensitive {
    name  = "s3.credentials.admin.secretKey"
    value = random_password.seaweedfs_secret_key[0].result
  }
}

resource "kubernetes_job_v1" "seaweedfs_buckets" {
  count = var.cloud == "azure" ? 1 : 0

  depends_on = [helm_release.seaweedfs]

  metadata {
    # Hash-keyed name: a changed bucket list creates a new job instead of
    # attempting an in-place update of the immutable job spec.
    name      = "seaweedfs-buckets-${substr(sha1(join(",", local.seaweedfs_buckets)), 0, 8)}"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
  spec {
    ttl_seconds_after_finished = 86400
    template {
      metadata {}
      spec {
        container {
          name  = "seaweedfs-buckets"
          image = "amazon/aws-cli:2.17.0"
          env {
            name = "AWS_ACCESS_KEY_ID"

            value_from {
              secret_key_ref {
                name = "seaweedfs-s3-secret"
                key  = "admin_access_key_id"
              }
            }
          }

          env {
            name = "AWS_SECRET_ACCESS_KEY"

            value_from {
              secret_key_ref {
                name = "seaweedfs-s3-secret"
                key  = "admin_secret_access_key"
              }
            }
          }
          env {
            name  = "AWS_DEFAULT_REGION"
            value = "us-east-1"
          }
          command = [
            "sh",
            "-c",
            join(" && ", [
              for b in local.seaweedfs_buckets :
              "aws --endpoint-url ${local.seaweedfs_s3_endpoint} s3api create-bucket --bucket ${b} || aws --endpoint-url ${local.seaweedfs_s3_endpoint} s3api head-bucket --bucket ${b}"
            ])
          ]
        }
        restart_policy = "Never"
      }
    }
    backoff_limit = 6
  }
  wait_for_completion = true
  timeouts {
    create = "10m"
    update = "10m"
  }
}
