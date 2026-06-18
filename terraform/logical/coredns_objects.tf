# In-cluster split-horizon DNS for the public object host (azure only). Browsers
# resolve s3.<domain> to the public LB (external-dns CNAME); in-cluster pods must
# resolve it to the Envoy Gateway service so the app's own object ops terminate
# TLS at the gateway (which fronts the plain-HTTP seaweedfs-s3:8333) instead of
# hairpinning to the Azure LB (unsupported). The presign signature is bound to the
# Host header (s3.<domain>), unchanged, so signatures stay valid.

locals {
  objects_public_host = var.cloud == "azure" && var.gateway_dns_label != "" ? "s3.${var.dns_domain_name}" : ""
  coredns_objects_on  = local.objects_public_host != ""
}

# Discover the Envoy Gateway data-plane Service (hashed name). Created by the Envoy
# Gateway controller once the Gateway exists; the app release (which creates the
# Gateway) is the dependency, so by apply time the Service exists.
data "kubernetes_resources" "envoy_gateway_svc" {
  count          = local.coredns_objects_on ? 1 : 0
  api_version    = "v1"
  kind           = "Service"
  namespace      = "envoy-gateway-system"
  label_selector = "gateway.envoyproxy.io/owning-gateway-name=${var.gateway_name}"
  depends_on     = [helm_release.app]
}

locals {
  gateway_svc_cluster_ip = local.coredns_objects_on ? try(data.kubernetes_resources.envoy_gateway_svc[0].objects[0].spec.clusterIP, "") : ""
}

# AKS merges *.override keys into the default CoreDNS server block. A hosts entry
# makes in-cluster lookups of the object host return the gateway ClusterIP;
# fallthrough preserves all other resolution. Count is gated on the static azure
# local (NOT the discovered IP) so it is known at plan time.
#
# AKS pre-creates an empty `coredns-custom` ConfigMap (addon-manager EnsureExists),
# so we PATCH its data rather than create the object (a create would 409). The
# _data resource manages only this key via server-side apply; AKS keeps owning the
# object and its labels.
resource "kubernetes_config_map_v1_data" "coredns_objects" {
  count = local.coredns_objects_on ? 1 : 0

  metadata {
    name      = "coredns-custom"
    namespace = "kube-system"
  }

  data = {
    "dozuki-objects.override" = <<-EOT
      hosts {
        ${local.gateway_svc_cluster_ip} ${local.objects_public_host}
        fallthrough
      }
    EOT
  }

  field_manager = "dozuki-mpc-objects"
  force         = true
}
