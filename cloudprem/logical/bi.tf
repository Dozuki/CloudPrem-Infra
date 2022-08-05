#resource "kubernetes_job" "dms_start" {
#  count = var.enable_bi ? 1 : 0
#
#  depends_on = [helm_release.replicated]
#
#  metadata {
#    name = "dms-start"
#  }
#  spec {
#    template {
#      metadata {}
#      spec {
#        container {
#          name  = "dms-start"
#          image = "bearengineer/awscli-kubectl:latest"
#          command = [
#            "/bin/sh",
#            "-c",
#            "kubectl -n ${coalesce([for i, v in data.kubernetes_all_namespaces.allns.namespaces : try(regexall("replicated\\-.*", v)[0], "")]...)} wait deploy/app-deployment --for condition=available --timeout=1200s && aws dms start-replication-task --start-replication-task-type start-replication --replication-task-arn ${var.dms_task_arn} --region ${data.aws_region.current.name}"
#          ]
#        }
#        restart_policy = "Never"
#      }
#    }
#    backoff_limit = 1
#    completions   = 1
#  }
#  wait_for_completion = true
#
#  timeouts {
#    create = "20m"
#  }
#}