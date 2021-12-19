data "aws_ssm_parameter" "dozuki_license" {
  name = local.dozuki_license_parameter_name
}

resource "random_password" "dashboard_password" {
  length  = 16
  special = true

  keepers = {
    nlb_dns_name = var.nlb_dns_name
  }
}

resource "kubernetes_config_map" "unattended_config" {
  metadata {
    name = "replicated-unattended-conf"
  }

  data = {
    "replicated.conf" = templatefile("${path.module}/static/replicated_config.json", {
      nlb_hostname       = random_password.dashboard_password.keepers.nlb_dns_name,
      release_sequence   = var.replicated_app_sequence_number,
      dashboard_password = random_password.dashboard_password.result
    })
  }
}

resource "helm_release" "replicated" {
  name       = "replicated"
  chart      = "${path.module}/charts/replicated"
  depends_on = [kubernetes_config_map.unattended_config]

  namespace = "default"

  set {
    name  = "license"
    value = data.aws_ssm_parameter.dozuki_license.value
  }
}

resource "kubernetes_job" "replicated_sequence_reset" {
  count = var.replicated_app_sequence_number > 0 ? 1 : 0

  depends_on = [helm_release.replicated]

  metadata {
    name = "replicated-sequence-reset"
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name  = "replicated-sequence-reset"
          image = "k8s.gcr.io/hyperkube:v1.17.9"
          command = [
            "/bin/sh",
            "-c",
            "kubectl wait deploy/retraced-api --for condition=available && kubectl exec deploy/replicated -- replicatedctl params set ReleaseSequence --value=0"
          ]
        }
        restart_policy = "Never"
      }
    }
    backoff_limit = 1
    completions   = 1
  }
  wait_for_completion = true
}