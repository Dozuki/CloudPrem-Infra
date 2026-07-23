# Istio ambient mesh (see the design doc referenced in the PR).
# Everything mesh-related lives in this file except: the NodePool startupTaints
# (kubernetes.tf, on the existing Karpenter manifests), the ratelimit redis
# NetworkPolicy HBONE port (ratelimit.tf), and the app release ordering
# (kubernetes.tf depends_on).

locals {
  istio_version    = "1.30.3"
  istio_chart_repo = "https://istio-release.storage.googleapis.com/charts"
  # Image hub override for airgapped installs. Gov phase 2 sets this to the gov ECR
  # mirror (source registry.istio.io/release; gcr.io retires 2027-01-01). Empty
  # string = chart default hub.
  istio_image_hub = ""

  mesh_state_rank = { disabled = 0, installed = 1, permissive = 2, strict = 3 }
  mesh_rank       = local.mesh_state_rank[var.istio_mesh_state]

  # Platform contract: ambient is validated on commercial AWS EKS Auto Mode only.
  # Gov joins after the haul pipeline mirrors istio images/charts (phase 2). Azure
  # is deferred (no node-taint surface on AKS today; see the design annex).
  mesh_supported = var.cloud == "aws" && !local.is_us_gov

  mesh_installed = local.mesh_rank >= 1
  mesh_enrolled  = local.mesh_rank >= 2
  mesh_strict    = local.mesh_rank >= 3
}

resource "kubernetes_namespace_v1" "istio_system" {
  count = local.mesh_installed ? 1 : 0
  metadata {
    name = "istio-system"
  }
}

resource "helm_release" "istio_base" {
  count = local.mesh_installed ? 1 : 0

  name       = "istio-base"
  namespace  = kubernetes_namespace_v1.istio_system[0].metadata[0].name
  repository = local.istio_chart_repo
  chart      = "base"
  version    = local.istio_version
  wait       = true

  # Istio CRDs are templated by this chart (enableCRDTemplates defaults true in
  # 1.30) and upgraded by Helm like ordinary resources - the opposite of Envoy
  # Gateway, whose CRDs must be applied out of band. Do NOT vendor istio CRDs.
  # Helm deliberately retains these CRDs on uninstall; teardown leaves them.
}

resource "helm_release" "istiod" {
  count = local.mesh_installed ? 1 : 0

  # Gateway API CRDs must exist before istiod (it watches them). The Envoy Gateway
  # CRD bundle already ships Gateway API v1.5.1, so istio and EG share those CRDs:
  # check istio compatibility on every EG CRD bump.
  depends_on = [kubectl_manifest.envoy_gateway_crds]

  name       = "istiod"
  namespace  = kubernetes_namespace_v1.istio_system[0].metadata[0].name
  repository = local.istio_chart_repo
  chart      = "istiod"
  version    = local.istio_version
  wait       = true
  timeout    = 600

  values = [yamlencode(merge(
    {
      profile = "ambient"
      # Untaint controller for the NodePool startupTaints. Top-level `taint` key
      # (NOT pilot.taint); it auto-sets PILOT_ENABLE_NODE_UNTAINT_CONTROLLERS.
      taint = { enabled = true }
      pilot = {
        # Autoscaling is on by default and ignores replicaCount. Min 2 so losing a
        # system node does not take the untaint controller down (dead istiod =
        # every new tainted custom-pool node stays unschedulable).
        autoscaleMin = 2
        # istiod lives on the built-in Auto Mode system pool: it must never depend
        # on the custom pools it untaints.
        nodeSelector = { "karpenter.sh/nodepool" = "system" }
        tolerations  = [{ key = "CriticalAddonsOnly", operator = "Exists" }]
        topologySpreadConstraints = [{
          maxSkew           = 1
          topologyKey       = "kubernetes.io/hostname"
          whenUnsatisfiable = "ScheduleAnyway"
          labelSelector     = { matchLabels = { app = "istiod" } }
        }]
      }
    },
    local.istio_image_hub == "" ? {} : { global = { hub = local.istio_image_hub } }
  ))]
}

resource "helm_release" "istio_cni" {
  count      = local.mesh_installed ? 1 : 0
  depends_on = [helm_release.istiod]

  name       = "istio-cni"
  namespace  = kubernetes_namespace_v1.istio_system[0].metadata[0].name
  repository = local.istio_chart_repo
  chart      = "cni"
  version    = local.istio_version
  wait       = true

  # No path overrides: EKS Auto Mode Bottlerocket uses the default
  # /opt/cni/bin + /etc/cni/net.d and istio-cni chains onto the managed VPC CNI.
  values = [yamlencode(merge(
    { profile = "ambient" },
    local.istio_image_hub == "" ? {} : { global = { hub = local.istio_image_hub } }
  ))]
}

resource "helm_release" "ztunnel" {
  count      = local.mesh_installed ? 1 : 0
  depends_on = [helm_release.istio_cni]

  name       = "ztunnel"
  namespace  = kubernetes_namespace_v1.istio_system[0].metadata[0].name
  repository = local.istio_chart_repo
  chart      = "ztunnel"
  version    = local.istio_version
  wait       = true

  # The ztunnel chart takes hub/tag at TOP level, not under global (verify in
  # Step 2; adjust if the rendered image is wrong).
  values = local.istio_image_hub == "" ? [] : [yamlencode({ hub = local.istio_image_hub })]
}
