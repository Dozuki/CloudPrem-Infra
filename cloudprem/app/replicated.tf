data "aws_ssm_parameter" "dozuki_license" {
  name = local.dozuki_license_parameter_name
}

resource "kubernetes_secret" "replicated_license" {
  metadata {
    name = "replicated-license"

    labels = {
      project   = "replicated"
      terraform = "true"
    }
  }

  data = {
    "license.rli" = data.aws_ssm_parameter.dozuki_license.value
  }
}

resource "kubernetes_config_map" "unattended_config" {
  metadata {
    name = "replicated-unattended-conf"
  }

  data = {
    "replicated.conf" = templatefile("${path.module}/static/replicated_config.json", {
      nlb_hostname       = var.nlb_dns_name,
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

  # There is a PVC that never gets to a Bound state
  //    wait = false

  set {
    name  = "license_secret"
    value = kubernetes_secret.replicated_license.metadata.0.name
  }
}
