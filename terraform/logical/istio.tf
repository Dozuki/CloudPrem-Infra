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

  # Platform contract: ambient runs on AWS EKS Auto Mode, both partitions. Gov
  # clusters pull docker.io/ghcr.io/quay.io today, so no image mirror is needed;
  # the old "phase-2 mirror first" gate was based on a stale airgap assumption.
  # Azure is deferred (no node-taint surface on AKS today; see the design annex).
  #
  # There is no lifecycle knob any more. The mesh used to walk a four-rung
  # ladder (disabled -> installed -> permissive -> strict) driven by a
  # var.istio_mesh_state input, so the fleet could be moved one rung per apply
  # with a canary env leading. That rollout finished 2026-08-08 with every
  # environment on strict, and the ladder was removed with it: install,
  # namespace enrollment and STRICT enforcement now all land in a single apply.
  # Two consequences worth knowing before changing this:
  #   - A first-time enable is no longer incremental. The carve-outs below and
  #     the depends_on chain order it correctly WITHIN one apply, but there is
  #     no longer an intermediate state to sit in and observe.
  #   - Rollback is a code change here, not an input flip. If STRICT has to come
  #     off in an incident, that is an edit + release + version bump per env.
  mesh_enabled = var.cloud == "aws"
}

resource "kubernetes_namespace_v1" "istio_system" {
  count = local.mesh_enabled ? 1 : 0
  metadata {
    name = "istio-system"
  }
}

resource "helm_release" "istio_base" {
  count = local.mesh_enabled ? 1 : 0

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

# Resource requests, no limits, on all three mesh components below (istiod,
# istio-cni, ztunnel). This is deliberate, not an oversight:
#
# ztunnel and istio-cni run as DaemonSets - one pod per node, in the pod's
# ambient dataplane path. An OOMKill on either drops the dataplane for every
# ambient pod on that node at once (ztunnel) or breaks CNI attach for new pods
# (istio-cni), so a limit that trips under a load spike is strictly worse than
# no limit: it turns a transient spike into a hard, self-inflicted outage.
# istiod is grouped here for the same reasoning even though it is not
# per-node: it is the mesh control plane, and an OOMKill there stalls cert
# rotation and config push cluster-wide until the replacement pod is ready.
#
# The actual protection is honest requests (sized to observed usage plus
# headroom below, so eviction ranking - which scores by usage over request -
# never picks these first) plus the spot NodePool's instance-memory floor
# (kubernetes.tf), which keeps any single node from being small enough that
# one hungry pod can starve everything else on it. Do not add limits here;
# that reintroduces the exact failure mode this PR closes.
resource "helm_release" "istiod" {
  count = local.mesh_enabled ? 1 : 0

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

  values = [yamlencode({
    profile = "ambient"
    # Untaint controller for the NodePool startupTaints. Top-level `taint` key
    # (NOT pilot.taint); it auto-sets PILOT_ENABLE_NODE_UNTAINT_CONTROLLERS.
    taint = { enabled = true }
    pilot = {
      # Autoscaling is on by default and ignores replicaCount. Min 2 so losing a
      # system node does not take the untaint controller down (dead istiod =
      # every new tainted custom-pool node stays unschedulable).
      autoscaleMin = 2
      # istiod runs on the on-demand pool, NOT the built-in Auto Mode system
      # pool. It used to sit on system, on the principle that the mesh control
      # plane must never depend on the custom pools it untaints. What that
      # actually bought was two dedicated nodes per environment running one
      # 100m pod each: the system pool is AWS-managed, so its nodes cannot be
      # sized from here, and Auto Mode picks 2 vCPU. Every one of those nodes
      # still paid the full per-node daemonset bill (~640m of 1780m allocatable)
      # to host 100m of istiod. Two nodes per env, fleet-wide, ~7.5x overhead.
      #
      # The bootstrap circularity that motivated the system pool does not
      # actually exist: the istiod chart adds a cni.istio.io/not-ready
      # toleration of its own, independent of the tolerations set here, so
      # istiod can land on a freshly-provisioned custom-pool node while that
      # node still carries the startupTaint istiod itself is responsible for
      # removing. Verified against running pods, which carry both that
      # toleration and the one configured below.
      #
      # on-demand rather than spot, deliberately: losing the mesh CA and the
      # untaint controller to a spot reclaim is not a trade worth making. The
      # capacity-type toleration is REQUIRED, not decorative - the on-demand
      # pool is tainted eks.amazonaws.com/capacity-type=on-demand:NoSchedule
      # (kubernetes.tf) while the spot pool is untainted, so changing the
      # nodeSelector without this toleration does not move istiod to spot, it
      # makes istiod unschedulable everywhere.
      nodeSelector = { "karpenter.sh/nodepool" = "on-demand" }
      tolerations = [
        {
          key      = "eks.amazonaws.com/capacity-type"
          operator = "Equal"
          value    = "on-demand"
          effect   = "NoSchedule"
        },
        # Pinned deliberately, even though the istiod chart adds this same
        # toleration on its own today. On the system pool istiod's ability to
        # schedule was structural: that pool carries no startupTaint, so there
        # was nothing to tolerate. On the on-demand pool it is conditional -
        # Karpenter stamps every new node with cni.istio.io/not-ready
        # (kubernetes.tf startupTaints) and istiod is the controller that
        # removes it. If a future chart bump ever drops the chart-side
        # toleration, a cluster with no running istiod would deadlock hard:
        # Karpenter brings up an on-demand node, the node keeps the startupTaint
        # because nothing can untaint it, istiod cannot schedule to do the
        # untainting, and the mesh never comes up. That is a bricked fresh
        # deploy, not a degradation, so the invariant is stated here rather than
        # inherited. Safe to set: pilot.tolerations appends, it does not replace
        # the chart's list (verified against running pods, which carry this
        # toleration while this repo previously set only CriticalAddonsOnly).
        {
          key      = "cni.istio.io/not-ready"
          operator = "Exists"
        },
      ]
      # DoNotSchedule + minDomains 2, not ScheduleAnyway. The original reasoning
      # was about a one-node system pool, where the nodeSelector restricted
      # eligible domains to a single node: with the default minDomains=1 the
      # observed-domain count already met the floor, skew was always 0, and any
      # number of istiod pods there satisfied maxSkew 1, so ScheduleAnyway never
      # had a second node to prefer. minDomains 2 (GA since k8s 1.30, fleet runs
      # 1.35.6) makes Pod Topology Spread treat a missing second domain as 0
      # pods, forcing the second replica to wait for a second node.
      #
      # On the on-demand pool the constraint is cheaper to satisfy and still
      # worth keeping: that pool already runs several nodes, and its
      # do-not-disrupt workloads hold a practical floor of ~2 (kubernetes.tf),
      # so minDomains 2 is normally met on arrival instead of forcing a
      # scale-from-zero. It still does the job it is here for - under STRICT
      # mTLS, one node hosting both replicas is a single point of failure for
      # the untaint controller and the mesh CA together, and on gov the SCP
      # denies ec2:TerminateInstances, so recovering a stuck node there is a
      # manual NodeClaim delete.
      topologySpreadConstraints = [{
        maxSkew           = 1
        minDomains        = 2
        topologyKey       = "kubernetes.io/hostname"
        whenUnsatisfiable = "DoNotSchedule"
        labelSelector     = { matchLabels = { app = "istiod" } }
      }]
      # Chart defaults are 500m/2Gi requests. Observed usage across 5 healthy
      # meshed envs tops out around 3m CPU / 59Mi memory (control-plane XDS
      # push load, not per-request traffic) - the default left roughly 30-150x
      # headroom over that. Right-sized to still keep several times observed
      # peak in reserve without reserving capacity nothing ever claims back.
      resources = {
        requests = {
          cpu    = "100m"
          memory = "256Mi"
        }
      }
    }
    # 1.30.3 defaults this on; pinned so a future chart default change cannot
    # drop istiod's PDB.
    global = merge(
      { defaultPodDisruptionBudget = { enabled = true } },
      local.istio_image_hub == "" ? {} : { hub = local.istio_image_hub }
    )
  })]
}

resource "helm_release" "istio_cni" {
  count      = local.mesh_enabled ? 1 : 0
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
    # Chart defaults (100m/100Mi requests, no limits - see the comment on
    # istiod above for why no limits). Observed usage across 5 healthy meshed
    # envs peaked around 37Mi memory with CPU pinned at the kubectl-top
    # rounding floor (1m) throughout, which undersells istio-cni's real
    # ceiling: its work is bursty node-attach/detach churn, not steady state,
    # so a quiet snapshot isn't a safe signal to size CPU down from. Memory
    # headroom is already several times observed peak. Pinned explicitly
    # (rather than left as an implicit chart default) so the value is a
    # deliberate, reviewed choice.
    { resources = { requests = { cpu = "100m", memory = "100Mi" } } },
    local.istio_image_hub == "" ? {} : { global = { hub = local.istio_image_hub } }
  ))]
}

resource "helm_release" "ztunnel" {
  count      = local.mesh_enabled ? 1 : 0
  depends_on = [helm_release.istio_cni]

  name       = "ztunnel"
  namespace  = kubernetes_namespace_v1.istio_system[0].metadata[0].name
  repository = local.istio_chart_repo
  chart      = "ztunnel"
  version    = local.istio_version
  wait       = true

  # The ztunnel chart takes hub/tag at TOP level, not under global (verify in
  # Step 2; adjust if the rendered image is wrong).
  #
  # Chart default is 200m/512Mi requests, no limits (see the comment on
  # istiod above for why no limits - this is the component that comment is
  # mainly about: today's incident was an unbounded app pod starving a small
  # spot node and taking its ztunnel down with it). Observed usage across 5
  # healthy meshed envs peaked around 102m CPU / 32Mi memory. CPU is left at
  # the chart default (already close to 2x peak, a sane margin). Memory is
  # brought down from 512Mi to 128Mi - still ~4x observed peak, but the
  # as-shipped 512Mi was reserving far more allocatable memory per spot node
  # than this workload has ever used, which works against the goal of this
  # PR (more usable headroom per small node for everything else scheduled
  # there).
  values = [yamlencode(merge(
    { resources = { requests = { cpu = "200m", memory = "128Mi" } } },
    local.istio_image_hub == "" ? {} : { hub = local.istio_image_hub }
  ))]
}

resource "kubernetes_labels" "ambient_dozuki" {
  count      = local.mesh_enabled ? 1 : 0
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
  count = local.mesh_enabled ? 1 : 0
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
  count = local.mesh_enabled ? 1 : 0
  # The NetworkPolicy must allow HBONE 15008 before redis-system is enrolled (and
  # enrollment must be removed before the rule on teardown).
  depends_on = [helm_release.ztunnel, kubernetes_network_policy_v1.ratelimit_redis]

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
    envoy-gateway-controller-webhook = {
      namespace = "envoy-gateway-system"
      selector = {
        "control-plane" = "envoy-gateway"
      }
      # API server -> topology-injector mutating webhook (:9443, failurePolicy
      # Ignore): silently loses zone-aware routing under STRICT without this.
      ports = [9443]
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
    metrics-server = {
      namespace = "dozuki"
      selector = {
        "app.kubernetes.io/name" = "metrics-server"
      }
      # metrics.k8s.io APIService callback on the chart's --secure-port. Same class
      # as prometheus-adapter above, and it arrived with #297/#151: retiring the EKS
      # addon moved metrics-server out of kube-system (unmeshed) and into the app
      # namespace, where namespace-wide STRICT rejects the API server's plain-HTTPS
      # probe. Symptom is `kubectl top` returning "Metrics API not available" and the
      # APIService going Available=False/FailedDiscoveryCheck, which leaves every
      # resource-metric HPA reading <unknown> and unable to scale. Custom-metric HPAs
      # keep working (they go via prometheus-adapter), so this hides well.
      ports = [10250]
    }
  }
  mesh_strict_namespaces = ["dozuki", "envoy-gateway-system", "redis-system"]
}

# Carve-outs must exist before namespace-wide STRICT lands (and outlive it on
# teardown), or the NLB-facing envoy ports and API-server webhook callbacks are
# rejected during the transition window.
resource "kubectl_manifest" "peer_auth_strict" {
  for_each   = local.mesh_enabled ? toset(local.mesh_strict_namespaces) : toset([])
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
  for_each = local.mesh_enabled ? local.mesh_carveouts : {}
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

# The ztunnel PodMonitor moved into the dozuki chart (templates/istio/ztunnel-podmonitor.yaml).
# There is no istio.ztunnelMonitor.enabled key anywhere in the chart. It renders on
# monitoring.enabled plus a Capabilities check for the istio ServiceEntry API, so it follows
# the mesh rollout on its own with no per-env flag from this layer. It belongs with its CRD,
# which ships in the chart's kube-prometheus-stack subchart - co-locating
# them removes the ordering hazard where this layer applied the PodMonitor before the app
# release had created the CRD.
