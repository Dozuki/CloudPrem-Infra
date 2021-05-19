resource "kubernetes_config_map" "unattended_config" {
  metadata {
    name = "replicated-unattended-conf"
  }

  data = {
    "replicated.conf" = templatefile("${path.module}/replicated.json", {
      nlb_hostname = var.nlb_hostname,
      release_sequence = var.release_sequence
    })
  }
}

resource "helm_release" "replicated" {
  name  = "replicated"
  chart = "${path.module}/charts/replicated"
  depends_on = [kubernetes_config_map.unattended_config]

  namespace = "default"

  # There is a PVC that never gets to a Bound state 
//  wait = false

  set {
    name  = "license_secret"
    value = kubernetes_secret.replicated_license.metadata.0.name
  }
}