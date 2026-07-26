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
                  # DMS refuses to start a task whose endpoints have never had a
                  # successful test-connection:
                  #   InvalidResourceStateFault: Test connection for replication instance
                  #   <ri> and endpoint <ep> should be successful for starting the
                  #   replication task
                  # On a FRESH stack that is always the case - nothing has ever tested
                  # them - so this is the normal path, not the rare post-DB-replace edge
                  # case the old comment assumed. Test both endpoints and wait for
                  # success before starting. test-connection is idempotent; an
                  # already-successful endpoint returns successful again.
                  RI=$(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-arn,Values=$ARN" --query 'ReplicationTasks[0].ReplicationInstanceArn' --output text)
                  for EP in $(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-arn,Values=$ARN" --query 'ReplicationTasks[0].[SourceEndpointArn,TargetEndpointArn]' --output text); do
                    ST=$(aws dms describe-connections --region "$REGION" --filters "Name=endpoint-arn,Values=$EP" "Name=replication-instance-arn,Values=$RI" --query 'Connections[0].Status' --output text 2>/dev/null)
                    if [ "$ST" != "successful" ]; then
                      echo "testing endpoint $EP (current: $ST)"
                      aws dms test-connection --replication-instance-arn "$RI" --endpoint-arn "$EP" --region "$REGION" >/dev/null 2>&1 || true
                    fi
                  done
                  for i in $(seq 1 30); do
                    PENDING=0
                    for EP in $(aws dms describe-replication-tasks --region "$REGION" --filters "Name=replication-task-arn,Values=$ARN" --query 'ReplicationTasks[0].[SourceEndpointArn,TargetEndpointArn]' --output text); do
                      ST=$(aws dms describe-connections --region "$REGION" --filters "Name=endpoint-arn,Values=$EP" "Name=replication-instance-arn,Values=$RI" --query 'Connections[0].Status' --output text 2>/dev/null)
                      [ "$ST" = "successful" ] || { PENDING=1; echo "endpoint $EP connection: $ST ($i/30)"; }
                    done
                    [ "$PENDING" -eq 0 ] && break
                    sleep 20
                  done

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
  # INTENTIONAL, do not flip to true. The job's first step waits for the app deployment to
  # become available, which on a fresh install is gated behind the db-migrations Job and can
  # take hours (dbMigrations.activeDeadlineSeconds is 14400). Blocking the apply on that would
  # wedge every logical run for the length of a migration. The cost is that a failed
  # dms-start leaves the run green, so DMS not starting is invisible here - check the Job
  # itself rather than the run status when replication is suspect.
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