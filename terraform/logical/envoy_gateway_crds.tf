# Envoy Gateway CRDs — managed here, NOT by the envoy_gateway helm_release.
#
# Why: Helm never upgrades CRDs that live in a chart's crds/ dir on `helm upgrade`
# (a deliberate Helm limitation), and EG's separate gateway-crds-helm chart can't
# be a helm_release because its rendered CRDs exceed Helm's 1MB release-secret
# limit. So on an EG version bump the chart's NEW CRDs never apply, the 1.8.x
# controller can't reconcile, and the release's `wait` times out (the failure that
# hit mpc-dev-min-logical). EG's own docs say: apply the CRDs first, then upgrade
# the gateway. We do exactly that, in-band, so there's no out-of-band manual step.
#
# Mechanism: the kubectl provider server-side-applies the vendored CRD set before
# the helm_release (depends_on). Server-side apply is REQUIRED — these CRDs are too
# large for client-side apply's 256KB last-applied annotation; force_conflicts lets
# us take ownership of CRDs Helm previously installed.
#
# Regenerating the vendored set on an EG version bump (bump local.envoy_gateway_version
# and drop in the new file):
#   helm template eg-crds oci://docker.io/envoyproxy/gateway-crds-helm --version <ver> \
#     --set crds.gatewayAPI.enabled=true --set crds.envoyGateway.enabled=true \
#     --set crds.gatewayAPI.channel=experimental \
#     | awk 'BEGIN{RS="\n---\n"} /kind: CustomResourceDefinition/{print "---"; print}' \
#     > crds/envoy-gateway-crds-<ver>.yaml
# (experimental channel matches what gateway-helm bundles — 20 CRDs; the
# ValidatingAdmissionPolicy is templated by gateway-helm itself, so we keep only CRDs.)

locals {
  envoy_gateway_version = "1.8.1"
}

data "kubectl_file_documents" "envoy_gateway_crds" {
  content = file("${path.module}/crds/envoy-gateway-crds-${local.envoy_gateway_version}.yaml")
}

resource "kubectl_manifest" "envoy_gateway_crds" {
  for_each  = data.kubectl_file_documents.envoy_gateway_crds.manifests
  yaml_body = each.value

  server_side_apply = true
  force_conflicts   = true
}
