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
  # istio_base must land first: it templates the Istio CRDs istiod watches.
  depends_on = [helm_release.istio_base, kubectl_manifest.envoy_gateway_crds]

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

resource "kubernetes_labels" "ambient_dozuki" {
  count      = local.mesh_enrolled ? 1 : 0
  depends_on = [helm_release.ztunnel]

  api_version = "v1"
  kind        = "Namespace"
  metadata {
    name = kubernetes_namespace_v1.app.metadata[0].name
  }
  labels = {
    "istio.io/dataplane-mode" = "ambient"
  }
  field_manager = "cpi-istio"
}

resource "kubernetes_labels" "ambient_envoy_gateway" {
  count = local.mesh_enrolled ? 1 : 0
  # envoy-gateway-system is created by the EG release (create_namespace), not by a
  # Terraform namespace resource.
  depends_on = [helm_release.ztunnel, helm_release.envoy_gateway]

  api_version = "v1"
  kind        = "Namespace"
  metadata {
    name = "envoy-gateway-system"
  }
  labels = {
    "istio.io/dataplane-mode" = "ambient"
  }
  field_manager = "cpi-istio"
}

resource "kubernetes_labels" "ambient_redis" {
  count      = local.mesh_enrolled ? 1 : 0
  depends_on = [helm_release.ztunnel]

  api_version = "v1"
  kind        = "Namespace"
  metadata {
    name = kubernetes_namespace_v1.ratelimit_redis.metadata[0].name
  }
  labels = {
    "istio.io/dataplane-mode" = "ambient"
  }
  field_manager = "cpi-istio"
}

# Verified against the rendered charts (ESO pin task + the vendored kps and
# prometheus-adapter tgz renders). Re-verify whenever any of those versions or
# the envoy-gateway version changes.
locals {
  mesh_carveouts = {
    envoy-gateway-proxy = {
      namespace = "envoy-gateway-system"
      selector = {
        "app.kubernetes.io/component"  = "proxy"
        "app.kubernetes.io/managed-by" = "envoy-gateway"
      }
      # NLB targets proxy pod IPs directly: client TLS passthrough on 10443,
      # plaintext redirects/ACME and NLB health checks on 10080.
      ports = [10443, 10080]
    }
    kps-operator-webhook = {
      namespace = "dozuki"
      selector = {
        "app"     = "kube-prometheus-stack-operator"
        "release" = "dozuki"
      }
      # API server admission webhook callback (HTTPS, cannot be mesh mTLS).
      ports = [10250]
    }
    external-secrets-webhook = {
      namespace = "dozuki"
      selector = {
        "app.kubernetes.io/name" = "external-secrets-webhook"
      }
      # API server admission webhook callback (HTTPS, cannot be mesh mTLS).
      ports = [10250]
    }
    prometheus-adapter = {
      namespace = "dozuki"
      selector = {
        "app.kubernetes.io/name" = "prometheus-adapter"
      }
      # external.metrics.k8s.io APIService callback; HPAs break without it.
      ports = [6443]
    }
  }
  mesh_strict_namespaces = ["dozuki", "envoy-gateway-system", "redis-system"]
}

# Carve-outs must exist before namespace-wide STRICT lands (and outlive it on
# teardown), or the NLB-facing envoy ports and API-server webhook callbacks are
# rejected during the transition window.
resource "kubectl_manifest" "peer_auth_strict" {
  for_each   = local.mesh_strict ? toset(local.mesh_strict_namespaces) : toset([])
  depends_on = [kubectl_manifest.peer_auth_carveouts]

  yaml_body = yamlencode({
    apiVersion = "security.istio.io/v1"
    kind       = "PeerAuthentication"
    metadata   = { name = "default", namespace = each.value }
    spec       = { mtls = { mode = "STRICT" } }
  })
  server_side_apply = true
}

resource "kubectl_manifest" "peer_auth_carveouts" {
  for_each = local.mesh_strict ? local.mesh_carveouts : {}
  depends_on = [
    kubernetes_labels.ambient_dozuki,
    kubernetes_labels.ambient_envoy_gateway,
    kubernetes_labels.ambient_redis,
  ]

  yaml_body = yamlencode({
    apiVersion = "security.istio.io/v1"
    kind       = "PeerAuthentication"
    metadata   = { name = each.key, namespace = each.value.namespace }
    spec = {
      selector = { matchLabels = each.value.selector }
      mtls     = { mode = "STRICT" }
      # JSON object keys are strings on the wire; the API server decodes them
      # into the CRD's port-number map.
      portLevelMtls = { for p in each.value.ports : tostring(p) => { mode = "PERMISSIVE" } }
    }
  })
  server_side_apply = true
}

resource "kubectl_manifest" "ztunnel_podmonitor" {
  count = local.mesh_enrolled ? 1 : 0
  # The PodMonitor CRD ships with the kube-prometheus-stack subchart INSIDE the
  # app release; applying this any earlier fails on fresh installs.
  depends_on = [helm_release.app, helm_release.ztunnel]

  yaml_body = yamlencode({
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "PodMonitor"
    metadata = {
      name      = "ztunnel"
      namespace = "istio-system"
      # kps Prometheus only discovers monitors carrying the release label.
      # Verified against the rendered vendored kps-82.8.0 chart (release
      # dozuki, ns dozuki): Prometheus.spec.podMonitorSelector.matchLabels =
      # {release: dozuki}, podMonitorNamespaceSelector: {} (select-all, so a
      # PodMonitor living in istio-system, outside the dozuki namespace, is
      # still discovered). No override of that selector exists anywhere in
      # the dozuki chart's values.yaml or in this logical layer's helm_release
      # "app" call, so the chart default stands.
      labels = { release = "dozuki" }
    }
    spec = {
      selector = { matchLabels = { app = "ztunnel" } }
      # Verified against the rendered ztunnel-1.30.3 chart: the DaemonSet pod
      # template carries label app: ztunnel, and the istio-proxy container
      # exposes containerPort 15020 named "ztunnel-stats" (not unnamed), so
      # port: (by name) is used as-is rather than falling back to targetPort.
      podMetricsEndpoints = [{
        port = "ztunnel-stats"
        path = "/stats/prometheus"
      }]
    }
  })
  server_side_apply = true
}
