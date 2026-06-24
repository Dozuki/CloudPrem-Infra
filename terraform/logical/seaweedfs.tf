# SeaweedFS object storage (Azure only) — provided by the dozuki chart's bundled
# seaweedfs subchart, exactly the way an on-prem install enables it
# (seaweedfs.enabled=true). Azure has no S3, so the app talks to the in-cluster
# SeaweedFS filer S3 gateway; on AWS the app uses real S3 (this whole file is a
# no-op when var.cloud != "azure").
#
# The previous standalone seaweedfs helm_release + bucket-creation Job were
# removed. The subchart now owns the master/volume/filer StatefulSets, the
# embedded S3 gateway Service (dozuki-seaweedfs-filer:8333), MySQL filer metadata,
# schema init, and bucket creation (filer.s3.createBuckets hook).

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

# Password for the dedicated MySQL user the filer uses for its metadata store.
# The chart's schema-init Job (running as the app DB admin) creates the
# seaweedfs_filer schema and this user before the filer starts.
resource "random_password" "seaweedfs_filer_db" {
  count = var.cloud == "azure" ? 1 : 0

  length  = 32
  special = false
}

locals {
  # In-cluster S3 endpoint. With filer.s3.enabled the subchart exposes the S3
  # gateway on the filer Service (named <release>-seaweedfs-filer => the release
  # is "dozuki") port 8333. This matches the chart's dozuki.objectStorageEndpoint
  # helper, which auto-wires the app to the same host when seaweedfs.enabled.
  seaweedfs_s3_endpoint = "http://dozuki-seaweedfs-filer.${local.k8s_namespace_name}.svc.cluster.local:8333"

  # The buckets the app uses. The subchart's native createBuckets hook is driven
  # from this same list so the created buckets always match objectStorage.*Bucket.
  seaweedfs_buckets = [
    for b in [
      var.s3_images_bucket,
      var.s3_objects_bucket,
      var.s3_documents_bucket,
      var.s3_pdfs_bucket,
    ] : b if b != ""
  ]

  # Values for the chart's bundled seaweedfs subchart. Merged into helm_release.app's
  # azure values in kubernetes.tf — and that merge only runs for var.cloud == "azure",
  # so this object is harmless on AWS (the try() guards the count=0 random_password
  # references). Keys mirror the chart's seaweedfs: section (chart/values.yaml).
  seaweedfs_values = {
    seaweedfs = {
      enabled = true
      mode    = "single"

      # S3 identity rendered into the dozuki-seaweedfs-s3 secret and loaded by the
      # filer S3 gateway; also fed to the app via objectStorage.credentials.
      s3 = {
        accessKey = try(random_password.seaweedfs_access_key[0].result, "")
        secretKey = try(random_password.seaweedfs_secret_key[0].result, "")
      }

      # MySQL filer metadata store. Reuses the app database server; the schema-init
      # Job runs as the app DB admin (adminUsername/adminPassword default to
      # db.user/db.password, set explicitly here) to create the seaweedfs_filer
      # schema and the dedicated seaweedfs user before the filer starts.
      mysqlFiler = {
        hostname      = local.db_master_host
        port          = 3306
        database      = "seaweedfs_filer"
        username      = "seaweedfs"
        password      = try(random_password.seaweedfs_filer_db[0].result, "")
        adminUsername = local.db_master_username
        adminPassword = local.db_master_password
      }

      # Subchart (upstream seaweedfs 4.31.0) overrides for AKS: pin PVCs to the
      # managed-csi StorageClass and size the volume servers from the CPI var.
      # volume.replicas=2 + 001 replication keeps a second copy so a single PVC
      # loss does not lose data (the durability posture the standalone deploy had).
      master = {
        data = { storageClass = "managed-csi" }
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
      }
      filer = {
        data = { storageClass = "managed-csi" }
        s3 = {
          # Drive the subchart's native bucket-creation hook from the CPI bucket
          # vars so created buckets always match objectStorage.*Bucket.
          createBuckets = [for b in local.seaweedfs_buckets : { name = b }]
        }
        # Point the filer's MySQL runtime config at the Azure app DB. Only the
        # hostname/database differ from the chart defaults (mysql/seaweedfs_filer);
        # username/password are injected by the subchart's own db-secret (patched by
        # the chart's filer-db-secret-patch-job from dozuki-seaweedfs-filer-config).
        extraEnvironmentVars = {
          WEED_MYSQL_HOSTNAME = local.db_master_host
          WEED_MYSQL_DATABASE = "seaweedfs_filer"
        }
      }
    }

    # NOTE: subchart replication lives under the chart's top-level global.seaweedfs
    # (global.seaweedfs.enableReplication / replicationPlacement). It is merged into
    # the existing `global` block in helm_release.app (kubernetes.tf) rather than set
    # here, because Terraform's merge() is shallow and a `global` key here would
    # clobber global.imagePullSecrets (the GHCR pull secret).

    # The HTTPRoute that exposes SeaweedFS S3 publicly (objectStorage.publicHost)
    # must target the embedded filer S3 service; the chart default backend
    # "seaweedfs-s3" only matches a standalone S3 deployment, which we do not run.
    objectStorage = {
      publicBackend = {
        service = "dozuki-seaweedfs-filer"
        port    = 8333
      }
    }
  }
}
