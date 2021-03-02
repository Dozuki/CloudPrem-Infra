resource "helm_release" "replicated" {
  name  = "replicated"
  chart = "${path.module}/charts/replicated"

  namespace = "default"

  # There is a PVC that never gets to a Bound state 
  wait = false

  set {
    name  = "license_secret"
    value = kubernetes_secret.replicated_license.metadata.0.name
  }
}