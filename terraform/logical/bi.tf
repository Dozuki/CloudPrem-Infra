resource "kubernetes_job_v1" "dms_start" {
  count = var.dms_enabled ? 1 : 0

  depends_on = [kubernetes_cluster_role_binding_v1.dozuki_list_role_binding]

  metadata {
    name      = "dms-start"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name = "dms-start"
          # TODO: pin to a digest and mirror into the airgap registries. ":latest" is
          # a supply-chain/reproducibility risk; unchanged here to keep this fix scoped.
          image = "bearengineer/awscli-kubectl:latest"
          command = [
            "/bin/sh",
            "-c",
            # The app deployment may not exist yet on a fresh install. The old
            # `kubectl wait deploy/...` hard-failed with NotFound in that window and
            # burned the Job's retries, so DMS never started (all 5 cutover envs had
            # to be started by hand). Wait for the deployment to EXIST first, then be
            # available. Then start the replication task idempotently — only if it is
            # not already running/starting, since start-replication-task errors on a
            # running task. (Edge case not handled: a source-endpoint connection gone
            # stale after a DB replace needs a test-connection first — see the cutover
            # runbook; rare, and outside this job's inputs.)
            <<-EOT
              set -e
              for i in $(seq 1 60); do
                kubectl get deploy/dozuki-app-deployment >/dev/null 2>&1 && break
                echo "waiting for dozuki-app-deployment to be created ($i/60)"; sleep 20
              done
              kubectl wait deploy/dozuki-app-deployment --for=condition=available --timeout=1200s
              ARN='${var.dms_task_arn}'
              REGION='${data.aws_region.current[0].region}'
              STATUS=$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-arn,Values=$ARN" --query 'ReplicationTasks[0].Status' --output text)
              echo "DMS replication task status: $STATUS"
              case "$STATUS" in
                running|starting) echo "task already running - nothing to do" ;;
                *) aws dms start-replication-task --start-replication-task-type start-replication --replication-task-arn "$ARN" --region "$REGION" ;;
              esac
            EOT
          ]
        }
        restart_policy = "Never"
      }
    }
    completions = 1
  }
  wait_for_completion = false

  timeouts {
    create = "20m"
  }
}

resource "kubernetes_config_map_v1" "grafana_create_db_script" {
  count = var.enable_bi ? 1 : 0
  metadata {
    name      = "grafana-create-db-script"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }

  data = {
    "grafana-db.sql" = file("static/grafana-db.sql")
  }
}

resource "kubernetes_secret_v1" "grafana_db_credentials" {
  count = var.enable_bi ? 1 : 0

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
  count = var.enable_bi ? 1 : 0

  metadata {
    name      = "grafana-db-create"
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
}

resource "random_password" "grafana_admin" {
  count = var.enable_bi ? 1 : 0

  length  = 16
  special = false
}