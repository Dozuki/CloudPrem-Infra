# Opt-in CiliumNetworkPolicy for the internet-facing Dozuki app tier.
#
# OFF by default (var.enable_network_policies) so existing stacks are unaffected; enabled
# per-environment only after the allow-list has been validated against that deployment.
# A default-deny policy with an incomplete allow-list breaks the app, so it is never flipped
# on for every customer in one change. Cilium-only: all self-managed-EKS (AWS) deployments
# run Cilium; it is a no-op on Azure.
#
# Selecting the app endpoint flips it to default-deny in both directions; we then allow
# exactly what the app needs (mapped from Hubble flows + the app's db.json/memcached.json/
# beanstalk.json/open-search.json config):
#   - DNS (kube-dns)
#   - the in-cluster backends it uses: memcached, beanstalkd, opensearch — and NOTHING else
#     in-cluster (this is the lateral-movement constraint: a compromised app pod can no longer
#     reach arbitrary pods/namespaces such as the ESO/Vault pods, the dashboard, etc.)
#   - the EKS kube-apiserver (the app's wait-for-migrations init container runs `kubectl wait`)
#   - off-cluster destinations (Aurora in-VPC, AWS APIs/S3 via VPC endpoints, IMDS/Pod-Identity,
#     SMTP) — these are world/host/remote-node to Cilium; allowed so the app stays functional.
# Ingress is restricted to the Envoy gateway (the only front door) plus node-sourced kubelet
# probes. Tightening external egress to specific FQDNs/ports is a documented follow-up.

locals {
  network_policies_enabled = var.cloud == "aws" && var.enable_network_policies
}

resource "kubernetes_manifest" "app_network_policy" {
  count = local.network_policies_enabled ? 1 : 0

  manifest = {
    apiVersion = "cilium.io/v2"
    kind       = "CiliumNetworkPolicy"
    metadata = {
      name      = "dozuki-app"
      namespace = local.k8s_namespace_name
    }
    spec = {
      endpointSelector = {
        matchLabels = { app = "app" }
      }

      ingress = [
        # The Envoy gateway is the only front door to the app.
        {
          fromEndpoints = [
            { matchLabels = { "k8s:io.kubernetes.pod.namespace" = "envoy-gateway-system" } },
          ]
          toPorts = [{
            ports = [
              { port = "80", protocol = "TCP" },
              { port = "443", protocol = "TCP" },
            ]
          }]
        },
        # kubelet liveness/readiness probes originate from the node.
        {
          fromEntities = ["host", "remote-node"]
        },
      ]

      egress = [
        # DNS — required for service and FQDN resolution.
        {
          toEndpoints = [
            { matchLabels = { "k8s:io.kubernetes.pod.namespace" = "kube-system", "k8s-app" = "kube-dns" } },
          ]
          toPorts = [{
            ports = [
              { port = "53", protocol = "UDP" },
              { port = "53", protocol = "TCP" },
            ]
          }]
        },
        # In-cluster backends the app talks to (and nothing else in-cluster).
        {
          toEndpoints = [
            { matchLabels = { "k8s:io.kubernetes.pod.namespace" = local.k8s_namespace_name, "app" = "dozuki-memcached" } },
          ]
          toPorts = [{ ports = [{ port = "11211", protocol = "TCP" }] }]
        },
        {
          toEndpoints = [
            { matchLabels = { "k8s:io.kubernetes.pod.namespace" = local.k8s_namespace_name, "app" = "beanstalkd" } },
          ]
          toPorts = [{ ports = [{ port = "11300", protocol = "TCP" }] }]
        },
        {
          toEndpoints = [
            { matchLabels = { "k8s:io.kubernetes.pod.namespace" = local.k8s_namespace_name, "app.kubernetes.io/name" = "opensearch" } },
          ]
          toPorts = [{ ports = [{ port = "9200", protocol = "TCP" }] }]
        },
        # EKS managed control plane (wait-for-migrations init container runs `kubectl wait`).
        {
          toEntities = ["kube-apiserver"]
        },
        # Off-cluster dependencies: Aurora (in-VPC), AWS APIs/S3 (VPC endpoints + public),
        # IMDS / Pod Identity, SMTP. All world/host/remote-node to Cilium. Allowed so the app
        # keeps working; the in-cluster egress constraint above is the security win.
        {
          toEntities = ["world", "host", "remote-node"]
        },
      ]
    }
  }
}
