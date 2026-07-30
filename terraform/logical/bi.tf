# The dms-start Job that used to live here is gone. It existed to start the BI replication
# task after the app deployment came up, and everything it did was task-shaped: it polled
# `aws dms describe-replication-tasks`, then ran `test-connection` against a REPLICATION
# INSTANCE before starting. Under DMS Serverless there is no replication instance to test
# against, `describe-replication-tasks` does not return a replication-config, and the
# replication starts itself - `start_replication = true` on aws_dms_replication_config, and
# serverless runs its own Testing Connection phase as part of provisioning.
#
# So this could not be ported, only deleted. Keeping it would have been worse than useless:
# wait_for_completion = false means its failure never surfaced in the run, which is exactly
# how DMS silently failing to start went unnoticed before (see the image and NotFound fixes
# in this file's history).

resource "kubernetes_config_map_v1" "grafana_create_db_script" {
  # Also created when enable_dashboards: the dashboards Grafana points at this grafana_primary
  # MySQL DB (see kubernetes.tf grafana.env.GF_DATABASE_*) instead of on-PVC SQLite. No-op where
  # enable_bi is already on (e.g. all 3M envs).
  count = (var.enable_bi || var.enable_dashboards) ? 1 : 0
  metadata {
    name      = "grafana-create-db-script"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }

  data = {
    "grafana-db.sql" = file("static/grafana-db.sql")
  }
}

resource "kubernetes_secret_v1" "grafana_db_credentials" {
  count = (var.enable_bi || var.enable_dashboards) ? 1 : 0

  metadata {
    name      = "grafana-db-credentials"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
  type = "Opaque"

  data = {
    host     = local.db_master_host
    username = local.db_master_username
    password = local.db_master_password
  }
}

resource "kubernetes_job_v1" "grafana_db_create" {
  count = (var.enable_bi || var.enable_dashboards) ? 1 : 0

  metadata {
    # Fold the primary DB's resourceId into the Job name (short sha256 suffix) so a database
    # replacement (new db_resource_id, e.g. a snapshot re-restore to a new cluster) yields a new
    # name and Terraform creates a fresh Job. The old static name meant the already-Completed Job
    # was never recreated on later applies, so grafana_primary never got created on the replaced
    # DB and the dashboards Grafana couldn't start. Same #4a fix the migration Job uses. When
    # db_resource_id is empty (default) the name stays "grafana-db-create" - no diff for stacks not
    # wiring it yet.
    name      = var.db_resource_id == "" ? "grafana-db-create" : "grafana-db-create-${substr(sha256(var.db_resource_id), 0, 8)}"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "grafana-db-create"
          image = "mysql:9.3"
          env {
            name = "MYSQL_HOST"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.grafana_db_credentials[0].metadata[0].name
                key  = "host"
              }
            }
          }
          env {
            name = "MYSQL_USER"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.grafana_db_credentials[0].metadata[0].name
                key  = "username"
              }
            }
          }
          env {
            name = "MYSQL_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.grafana_db_credentials[0].metadata[0].name
                key  = "password"
              }
            }
          }
          command = [
            "sh",
            "-c",
            "mysql --host=$MYSQL_HOST --user=$MYSQL_USER --password=$MYSQL_PASSWORD < /scripts/grafana-db.sql"
          ]
          volume_mount {
            name       = "scripts"
            mount_path = "/scripts"
            read_only  = true
          }
        }
        volume {
          name = "scripts"
          config_map {
            name = kubernetes_config_map_v1.grafana_create_db_script[0].metadata[0].name
          }
        }
        restart_policy = "OnFailure"
      }
    }
    backoff_limit = 50
  }
  wait_for_completion = true

  # Without this the provider's default create timeout applies, which a fresh
  # deploy blows through: the pod has to pull mysql:9.3 from Docker Hub onto a
  # just-provisioned node and then reach Aurora. Existing stacks never saw it —
  # the job is only created once and isn't recreated on later applies — so it
  # only bites brand-new BI-enabled deploys.
  timeouts {
    create = "15m"
  }
}

resource "random_password" "grafana_admin" {
  count = var.enable_bi ? 1 : 0

  length  = 16
  special = false
}