# Envoy Gateway CRDs — managed here, NOT by the envoy_gateway helm_release.
#
# Why: Helm never upgrades CRDs that live in a chart's crds/ dir on `helm upgrade`
# (a deliberate Helm limitation, unchanged in Helm 4 — its --skip-crds help still
# reads "installed if not already present"). So on an EG version bump the chart's NEW
# CRDs never apply, the 1.8.x controller can't reconcile, and the release's `wait`
# times out (the failure that hit mpc-dev-min-logical). EG's own docs say: apply the
# CRDs first, then upgrade the gateway. We do exactly that, in-band, so there's no
# out-of-band manual step.
#
# Upstream's answer to the same problem is the separate gateway-crds-helm chart, which
# holds the CRDs as TEMPLATES rather than in a crds/ dir, so Helm does upgrade them.
# We cannot use it, and neither can anyone else on the default storage driver: Helm
# stores a release as base64(gzip(json)) in a Secret, and Kubernetes hard-rejects Secret
# data over 1048576 DECODED bytes ("data: Too long: may not be more than 1048576
# bytes"). Because gateway-crds-helm's CRDs are templates they are stored twice, as
# base64 template source in the chart record AND as the rendered manifest, which lands
# at roughly 1.6 to 1.85MB decoded, 1.5x to 1.8x over the ceiling. That is a Helm
# storage limit, not a Terraform one: installing it by hand fails the same way. Only
# HELM_DRIVER=sql escapes it, which is not worth a Postgres dependency for CRDs.
#
# This is upstream envoyproxy/gateway#6105, open since 2025-05 and still live. The
# maintainer's own recommendation there is `helm template` plus an apply, which is
# exactly what this file does, wired in-band so there is no manual step. Note that the
# chart's crds.gatewayAPI.enabled=false escape hatch does NOT get you under the limit
# (reported against 1.8.1 in that issue): Helm stores every template file in the chart
# whether it rendered or not, so the payload is there regardless of the toggles.
# Flux users have no workaround at all, having no templating step.
#
# skip_crds below is also what keeps THIS release's record small, which is a second
# reason to keep it. Measured on mpc-dev-min-logical: the pre-#201 revisions carry the
# 4.4MB crds/ payload in the chart record and sit at 1,035,308 decoded bytes (98.7% of
# the limit), while every revision since is 27,276 bytes (2.6%).
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
# ValidatingAdmissionPolicy is templated by gateway-helm itself, so we keep only CRDs.
# It lives in the chart's crds SUBCHART templates/ dir, not in a crds/ dir, which is
# why skip_crds on the release does not suppress it — see the crds.enabled note below.)
#
# Do NOT set the chart's crds.enabled=false (added in EG 1.8.2). It looks like the
# right switch for "CRDs are managed separately", but it disables the whole crds
# subchart, which also renders the Gateway API safe-upgrade ValidatingAdmissionPolicy
# and its Binding. skip_crds already skips the CRD payload while keeping that policy.

locals {
  envoy_gateway_version = "1.8.3"
}

data "kubectl_file_documents" "envoy_gateway_crds" {
  content = file("${path.module}/crds/envoy-gateway-crds-${local.envoy_gateway_version}.yaml")
}

resource "kubectl_manifest" "envoy_gateway_crds" {
  for_each  = data.kubectl_file_documents.envoy_gateway_crds.manifests
  yaml_body = each.value

  server_side_apply = true
  force_conflicts   = true

  # Keeps the CRD schemas out of the run log. yaml_body is already sensitive in the
  # provider schema, but the computed yaml_body_parsed is not, so on any create or
  # update of these 20 resources it printed each CRD in full: the first gov logical
  # build was 120k lines / 16MB of plan output, against 1.9k lines for the same stack
  # once the CRDs were unchanged. sensitive_fields obfuscates the named subtree in
  # yaml_body_parsed, so the plan shows which CRD is changing (apiVersion, kind,
  # metadata) without the schema body. spec is masked as a whole because the provider
  # only supports masking map values, not list members, and the schemas hang off
  # spec.versions[].schema.
  #
  # This is display-only: it does not change what is applied (that is ignore_fields)
  # and drift detection still works off yaml_body/yaml_incluster, which both change
  # normally. Review CRD content in the git diff of the vendored file, not the plan.
  sensitive_fields = ["spec"]
}
