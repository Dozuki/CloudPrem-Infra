# Cilium + Hubble Grafana dashboards (v1.17, vendored from the Cilium repo under
# files/cilium-dashboards/, datasource pinned to the in-cluster Prometheus). The
# kube-prometheus-stack Grafana auto-loads any ConfigMap labelled grafana_dashboard=1
# via its dashboard sidecar (it watches all namespaces), so these appear pre-loaded on
# every deploy. AWS/self-managed only — the metrics they chart come from the Cilium
# agent/operator/Hubble exporters enabled in the physical cilium bootstrap.
locals {
  cilium_dashboards = var.cloud == "aws" ? {
    cilium-agent    = "${path.module}/files/cilium-dashboards/cilium-agent.json"
    cilium-operator = "${path.module}/files/cilium-dashboards/cilium-operator.json"
    hubble          = "${path.module}/files/cilium-dashboards/hubble.json"
  } : {}
}

resource "kubernetes_config_map_v1" "cilium_dashboard" {
  for_each = local.cilium_dashboards

  metadata {
    name      = "cilium-dashboard-${each.key}"
    namespace = local.k8s_namespace_name
    labels = {
      grafana_dashboard = "1"
    }
  }

  data = {
    "${each.key}.json" = file(each.value)
  }
}
