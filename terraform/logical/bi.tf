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
          # Was bearengineer/awscli-kubectl:latest, which ships aws-cli 1.16.234 /
          # botocore 1.12.224 (2019). That predates EKS Pod Identity, so every
          # credential lookup died with:
          #   Unsupported host '169.254.170.23'. Can only retrieve metadata from
          #   these hosts: 169.254.170.2, localhost, 127.0.0.1
          # 169.254.170.23 is the Pod Identity agent; 169.254.170.2 is the old ECS
          # endpoint that botocore knew about. The job therefore could never call
          # DMS, and because wait_for_completion is false the apply still reported
          # success — which is why DMS silently never started and cutover envs kept
          # having to be started by hand.
          #
          # alpine/k8s carries both kubectl and aws (botocore 1.35, well past the
          # ~1.33 that added Pod Identity support) and is pinned, which also retires
          # the ":latest" supply-chain TODO this line used to carry.
          image = "alpine/k8s:1.31.0"
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
                creating|modifying|stopping|testing|deleting) echo "transient state '$STATUS' - a later reconcile will act" ;;
                None|"") echo "replication task not found: $ARN" >&2; exit 1 ;;
                ready|stopped|failed)
                  # start-replication redoes a full load - intended for the cutover
                  # restore-from-new-primary path. For a steady-state task known to have
                  # completed its full load, resume-processing (CDC from last stop) is
                  # cheaper; refine if this job is ever used purely for restart.
                  aws dms start-replication-task --start-replication-task-type start-replication --replication-task-arn "$ARN" --region "$REGION" ;;
                *) echo "unexpected task status '$STATUS'" >&2; exit 1 ;;
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