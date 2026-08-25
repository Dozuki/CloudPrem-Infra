resource "kubernetes_storage_class_v1" "ebs_gp3" {
  count = var.cloud == "aws" ? 1 : 0

  metadata {
    name = "ebs-gp3"
    annotations = {
      "storageclass.kubernetes.io/is-default-class" = "true"
    }
  }
  storage_provisioner    = "ebs.csi.eks.amazonaws.com"
  reclaim_policy         = "Delete"
  volume_binding_mode    = "WaitForFirstConsumer"
  allow_volume_expansion = true
  parameters = merge(
    {
      type      = "gp3"
      encrypted = "true"
    },
    # Dynamically provisioned volumes are created by the CSI controller, not the
    # AWS provider, so default_tags (and the harness deleteAfter TTL) never reach
    # them; a failed teardown leaves them orphaned forever. StorageClass
    # parameters are immutable, but delete_after is only ever set on ephemeral
    # harness deploys where the class is created fresh each run.
    var.delete_after != "" ? { tagSpecification_1 = "deleteAfter=${var.delete_after}" } : {},
  )
}

resource "kubernetes_namespace_v1" "app" {
  metadata {
    name = local.k8s_namespace_name
  }

  lifecycle {
    # The ambient enrollment label is owned by kubernetes_labels.ambient_dozuki
    # (istio.tf) under its own field manager. Without this, every apply strips
    # the label and silently un-enrolls the namespace from the mesh (mTLS stops
    # being enforced with no error anywhere). Caught live on the min pilot.
    ignore_changes = [metadata[0].labels["istio.io/dataplane-mode"]]
  }
}

resource "kubernetes_role_v1" "dozuki_subsite_role" {
  metadata {
    name      = "dozuki_subsite_role"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }

  rule {
    api_groups = ["infra.dozuki.com"]
    resources  = ["subsites"]
    verbs      = ["get", "list", "watch", "create", "delete"]
  }
}


resource "kubernetes_role_binding_v1" "dozuki_subsite_role_binding" {

  metadata {
    name      = "dozuki_subsite_role_binding"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role_v1.dozuki_subsite_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
}

resource "kubernetes_cluster_role_v1" "dozuki_list_role" {

  metadata {
    name = "dozuki_list_role"
  }

  rule {
    api_groups = ["apps"]
    resources  = ["deployments", "daemonsets"]
    verbs      = ["get", "list", "watch"]
  }
  rule {
    api_groups = [""]
    resources  = ["pods"]
    verbs      = ["list"]
  }
}

resource "kubernetes_cluster_role_binding_v1" "dozuki_list_role_binding" {

  metadata {
    name = "dozuki_list_role_binding"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role_v1.dozuki_list_role.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = "default"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }
}

resource "kubernetes_namespace_v1" "cert_manager" {
  metadata {
    name = "cert-manager"
  }
}

resource "helm_release" "cert_manager" {
  name  = "cert-manager"
  chart = "${path.module}/charts/cert-manager"

  namespace = kubernetes_namespace_v1.cert_manager.metadata[0].name

  # wait=true now covers 3 webhook pods instead of 1, and on a fresh Auto Mode cluster
  # cert-manager is the workload that triggers the first node provisioning, so the rollout can
  # outlast the provider's 300s default. Raised to match the envoy-gateway release below, which
  # hit the same class of timeout on Auto Mode.
  wait    = true
  timeout = 600

  # Webhook availability. cert-manager's webhook is failurePolicy=Fail and sits in the
  # deploy path: while it has no endpoints, every create/patch of ANY cert-manager object
  # fails outright, whoever is applying it. At one replica with no PDB a single Karpenter
  # consolidation eviction takes it out for 30-60s, and on 2026-08-12 that window landed
  # inside the Flux chart upgrade on dev-min, which was patching the app chart's cert-issuer
  # ClusterIssuer at the time (that object belongs to the dozuki chart, not to this release):
  # "cannot patch cert-issuer with kind ClusterIssuer: failed calling webhook
  # webhook.cert-manager.io: no endpoints available for service cert-manager-webhook".
  # The release failed and stalled.
  #
  # 3 replicas + a minAvailable=1 PDB is upstream's production recommendation
  # (https://cert-manager.io/docs/installation/best-practice/). Deliberately NOT a
  # karpenter.sh/do-not-disrupt pin: a pin holds its node out of consolidation permanently,
  # which is the cost this rightsizing round exists to remove. Order matters - at ONE replica
  # a minAvailable=1 PDB is worse than nothing: it makes the eviction API refuse every
  # request, and the node only finishes draining when its terminationGracePeriod expires and
  # the pod is force-deleted (EKS Auto Mode stamps 24h). The replica raise is what turns the
  # PDB into a real instrument rather than a 24h stall.
  #
  # Known edge, accepted: on a cluster with no spare capacity, all 3 replicas can sit on one
  # node (the spread below is soft), and draining that node without a replacement stalls once
  # the last replica would break minAvailable. Ordinary Karpenter consolidation is not exposed
  # to this - it brings the replacement node up before it drains - and no environment in this
  # fleet runs near a single node.
  #
  # priorityClassName is chart-global in v1.19.4 (there is no webhook-scoped key), so it also
  # lands on the controller and cainjector. That is what upstream's best-practice guide
  # prescribes for all three components anyway.
  #
  # The topology spread is soft (ScheduleAnyway) on purpose: three replicas packed onto one
  # node would satisfy the PDB while a single node loss still took the whole webhook down.
  # Soft keeps them schedulable on a small cluster instead of going Pending.
  values = [yamlencode({
    global = {
      priorityClassName = "system-cluster-critical"
    }
    webhook = {
      replicaCount = 3
      podDisruptionBudget = {
        enabled      = true
        minAvailable = 1 # never set maxUnavailable too - the chart renders both and the API rejects that
      }
      topologySpreadConstraints = [{
        maxSkew           = 1
        topologyKey       = "kubernetes.io/hostname"
        whenUnsatisfiable = "ScheduleAnyway"
        # The chart dumps this list into the pod spec verbatim and fills in NO selector of its
        # own, so without these matchLabels the constraint is silently inert. Release-name
        # labels are deliberately left out: keying on app.kubernetes.io/instance would make a
        # rename turn this into a no-op instead of an error. name+component is unique to the
        # webhook pods in this namespace.
        labelSelector = { matchLabels = {
          "app.kubernetes.io/name"      = "webhook"
          "app.kubernetes.io/component" = "webhook"
        } }
      }]
    }
  })]

  set = [
    {
      name  = "crds.enabled"
      value = "true"
    },
    {
      name  = "crds.keep"
      value = "true"
    },
    {
      name  = "config.enableGatewayAPI"
      value = "true"
    },
  ]
}

resource "helm_release" "envoy_gateway" {
  name       = "envoy-gateway"
  namespace  = "envoy-gateway-system"
  repository = "oci://docker.io/envoyproxy"
  chart      = "gateway-helm"
  version    = "v${local.envoy_gateway_version}"

  # create_namespace = true: Helm owns the envoy-gateway-system namespace.
  # The redis-auth secret in that namespace (kubernetes_secret_v1.redis_auth_eg)
  # depends_on this release so it's written after the namespace exists.
  create_namespace = true
  wait             = true

  # CRDs are NOT managed by this release (skip_crds). Helm can't upgrade CRDs in a
  # chart's crds/ dir on `helm upgrade`, so they're server-side-applied first by
  # kubectl_manifest.envoy_gateway_crds (envoy_gateway_crds.tf). depends_on enforces
  # EG's required "CRDs before gateway" upgrade order. timeout is raised above
  # Helm's 300s default — the EG controller rollout on Auto Mode can exceed it
  # (that timeout is what failed mpc-dev-min-logical before CRDs were managed here).
  skip_crds = true
  timeout   = 600

  # Controller config:
  #  - extensionApis.enableEnvoyPatchPolicy: required by the chart's GeoIP feature
  #    (gateway.geoip.enabled injects an EnvoyPatchPolicy).
  #  - rateLimit.backend -> in-cluster Redis (see ratelimit.tf) so the chart's
  #    rate-limit BackendTrafficPolicies actually enforce (otherwise inert).
  #  - provider.kubernetes.rateLimitDeployment.container.env injects REDIS_AUTH
  #    from the redis-auth Secret (in redis-system) into the envoy-ratelimit pod
  #    via valueFrom.secretKeyRef. The envoy-ratelimit binary reads REDIS_AUTH
  #    and passes it as the Redis AUTH password. No plaintext password in the
  #    EnvoyGateway ConfigMap. See ratelimit.tf for the Secret + Redis --requirepass.
  values = [yamlencode({
    # Controller (control-plane) resources. The gateway-helm default is a 100m CPU
    # request, which crashlooped the controller under load: CPU-starved on a busy
    # node it couldn't answer its 1s liveness probe in time, kubelet killed it, and
    # every kill dropped the xDS stream so the whole data plane went down (took one
    # env down mid-cutover). A 500m request gives it guaranteed scheduling headroom so the
    # probe stays responsive; no CPU limit so reconciliation can burst. (The chart
    # does not expose the controller's probe timeouts, so we fix this via resources;
    # verify this value path against the pinned gateway-helm chart on version bumps.)
    # Memory trimmed to the measured value (14d, 9 envs: max working set 158Mi, x1.2
    # rounded up = 192Mi). CPU stays 500m - a right-sizing pass measured p95 25.4m and
    # proposed 40m, which is precisely the starvation the paragraph above describes.
    # We know 500m does not crashloop; we do not know what value starts to. Do not
    # trade a known-good incident fix for CPU request that buys no nodes.
    deployment = {
      envoyGateway = {
        resources = {
          requests = {
            cpu    = "500m"
            memory = "192Mi"
          }
        }
      }
    }
    config = {
      envoyGateway = {
        extensionApis = {
          enableEnvoyPatchPolicy = true
        }
        provider = {
          type = "Kubernetes"
          kubernetes = {
            rateLimitDeployment = {
              container = {
                # gateway-helm ships this at 100m/512Mi. Measured over 14d across 9
                # envs it runs p95 16.3m of CPU against a 28Mi working set - the 512Mi
                # request alone was reserving half a gigabyte per env for a process
                # that never touches it, the largest single overstatement in the fleet.
                resources = {
                  requests = {
                    cpu    = "25m"
                    memory = "64Mi"
                  }
                }
                env = [
                  {
                    name = "REDIS_AUTH"
                    valueFrom = {
                      secretKeyRef = {
                        name = "redis-auth"
                        key  = "password"
                      }
                    }
                  }
                ]
              }
            }
          }
        }
        rateLimit = {
          backend = {
            type = "Redis"
            redis = {
              url = "redis.redis-system.svc.cluster.local:6379"
            }
          }
        }
      }
    }
  })]

  depends_on = [
    kubernetes_secret_v1.redis_auth,
    kubernetes_service_v1.ratelimit_redis,
    # CRDs must be applied before the gateway upgrade (Helm can't upgrade them) —
    # see envoy_gateway_crds.tf.
    kubectl_manifest.envoy_gateway_crds,
  ]
}

# The stable dozuki-envoy-proxy Service (AWS ClusterIP / Azure LoadBalancer, in
# envoy-gateway-system) now ships in the chart (gateway.stableProxyService, set below), so
# it renders in-release next to the Gateway it fronts. It stayed in Terraform only for
# historical ordering; nothing here needed a physical input. The AWS TargetGroupBindings
# below stay in Terraform - they bind to the physical NLB target-group ARN.

# Azure only: the ingress_ip output needs the LoadBalancer IP the chart's Service is assigned.
# Read it back rather than owning the Service. depends_on the release so the read happens after
# the Service exists; try() in the output tolerates the brief window before the LB IP lands.
data "kubernetes_service_v1" "envoy_proxy_azure" {
  count = var.cloud == "azure" ? 1 : 0
  # App is Flux-delivered now; anchor on the HelmRelease CR. Best-effort: the CR apply returns on
  # creation, not reconcile, so the Service may not exist on the first read - try() in the ingress_ip
  # output tolerates that window and the next apply resolves it.
  depends_on = [kubectl_manifest.dozuki_helmrelease]

  metadata {
    name      = "dozuki-envoy-proxy"
    namespace = "envoy-gateway-system"
  }
}

# The envoy-https / envoy-http TargetGroupBindings moved into the dozuki chart
# (templates/gateway/target-group-bindings.yaml), next to the stable proxy Service they bind.
# This layer just passes the physical target-group ARNs as chart values (see the
# gateway.stableProxyService.targetGroupBindings set entries below).

# EKS Auto Mode creates and reconciles a built-in NodeClass named "default" the
# moment compute_config is enabled (terraform/physical/eks.tf), and the NodePool
# below pointed at it unmodified. That built-in class carries no spec.tags field
# at all, and it is Karpenter (a controller), not the AWS provider, that calls
# CreateFleet for every node - so infra-live's root.hcl default_tags, which tags
# everything the AWS provider itself creates, never reaches a single EC2 instance
# Karpenter launches. Every Auto Mode node in the fleet has shipped untagged since
# day one, which leaves node compute unattributable to a customer or environment
# in cost reporting. The EKS Auto Mode per-node management-hours surcharge lands
# untagged for the same reason. (Figures are tracked internally, not here: this
# repo is public.)
#
# Fix: read the built-in class's fields back (nothing here is safe to hardcode -
# spec.role in particular is an EKS-generated name with a random suffix, unique
# per cluster) and republish them on our own NodeClass, adding spec.tags. The
# NodePool then points at ours instead of "default".
data "kubernetes_resource" "nodeclass_default" {
  count = var.cloud == "aws" ? 1 : 0

  api_version = "eks.amazonaws.com/v1"
  kind        = "NodeClass"

  metadata {
    name = "default"
  }
}

resource "kubernetes_manifest" "nodeclass_dozuki" {
  count = var.cloud == "aws" ? 1 : 0

  manifest = {
    apiVersion = "eks.amazonaws.com/v1"
    kind       = "NodeClass"
    metadata = {
      name = "dozuki"
      # Deliberately NOT labeled app.kubernetes.io/managed-by: eks - that label is
      # what EKS stamps on the class IT owns and reconciles; putting it on ours
      # would be a lie about who manages this object and could confuse the Auto
      # Mode controller into thinking it should also reconcile this one.
    }
    spec = {
      # Copied through from the built-in class rather than restated as literals,
      # so an EKS-side change to networking/role/storage defaults doesn't
      # silently diverge between the two classes on the next apply.
      role                       = data.kubernetes_resource.nodeclass_default[0].object.spec.role
      subnetSelectorTerms        = data.kubernetes_resource.nodeclass_default[0].object.spec.subnetSelectorTerms
      securityGroupSelectorTerms = data.kubernetes_resource.nodeclass_default[0].object.spec.securityGroupSelectorTerms
      ephemeralStorage           = data.kubernetes_resource.nodeclass_default[0].object.spec.ephemeralStorage
      networkPolicy              = data.kubernetes_resource.nodeclass_default[0].object.spec.networkPolicy
      networkPolicyEventLogs     = data.kubernetes_resource.nodeclass_default[0].object.spec.networkPolicyEventLogs
      snatPolicy                 = data.kubernetes_resource.nodeclass_default[0].object.spec.snatPolicy
      # The one field the built-in class doesn't have and the whole reason this
      # resource exists. local.tags mirrors the AWS provider's own default_tags
      # (main.tf), so every node this class launches carries the same
      # Service/Customer/Environment keys as every other resource in the stack.
      tags = local.tags
    }
  }

  lifecycle {
    precondition {
      # role is EKS-generated per cluster (see the comment above) - if the data
      # source above ever comes back without one, fail the plan loudly instead of
      # creating a NodeClass with a blank IAM role, which Karpenter would accept
      # but every node launch against it would then fail silently at CreateFleet.
      condition     = try(data.kubernetes_resource.nodeclass_default[0].object.spec.role, "") != ""
      error_message = "data.kubernetes_resource.nodeclass_default returned an empty (or missing) spec.role. Refusing to create kubernetes_manifest.nodeclass_dozuki with a blank node IAM role - that would silently fail to provision nodes. Confirm the built-in EKS Auto Mode NodeClass \"default\" exists and compute_config is enabled (terraform/physical/eks.tf) before re-applying."
    }

    precondition {
      # local.tags mirrors whatever root.hcl injects, which means a tag added
      # there lands here without anyone editing this file. EKS Auto Mode rejects
      # a NodeClass whose spec.tags carries "Name" or a key under one of the
      # reserved prefixes, and it rejects it at ADMISSION - so a bad key would
      # not fail here, it would fail the apply in every environment at once with
      # a message about the CRD rather than about root.hcl. As of 2026-08-25 the
      # six injected keys (Environment, Customer, Service, ManagedBy, StackPath,
      # Repo) are all fine; this exists so the seventh is caught in plan.
      condition = length([
        for key in keys(local.tags) : key
        if key == "Name" || anytrue([
          for reserved in ["kubernetes.io/", "eks.amazonaws.com/", "karpenter.sh/", "karpenter.k8s.aws/"] :
          startswith(key, reserved)
        ])
      ]) == 0
      error_message = "local.tags contains a tag key EKS Auto Mode reserves and will reject on a NodeClass (\"Name\", or a key under kubernetes.io/, eks.amazonaws.com/, karpenter.sh/ or karpenter.k8s.aws/). These tags come from the AWS provider default_tags that infra-live's root.hcl injects, so fix it there rather than here."
    }
  }
}

# ONE custom NodePool per env. There were two (a weight-100 "spot" pool and this
# tainted "on-demand" pool); the split is what held every env at ~6 nodes, because
# each pool floors independently and neither can pack into the other. Collapsing
# them lets an env converge on its real minimum (~3, floored by the opensearch
# zonal spread) - which is what the Datadog per-host bill actually tracks.
#
# The pool is still named "on-demand" and that name is LOAD-BEARING, not cosmetic:
#   - the dozuki chart hard-codes it (`dozuki.onDemandNodeSelector` renders
#     `karpenter.sh/nodepool: on-demand`, and `spotPreferredBoundedNodeAffinity`
#     requires `nodepool In [spot, on-demand]`), and
#   - flux.tf's local.app_stateful_scheduling and istio.tf's istiod pin select on it.
# Renaming it makes those pods unschedulable, and because metadata.name is
# immutable it would be a destroy+create that drains every node in the pool.
# On a spot-preferred env the name is also simply historical - the pool buys spot
# there. Retiring the name is follow-up work, after the fleet has converged.
#
# Two comment blocks elsewhere go stale the moment this ships and are deliberately
# NOT edited here, because touching those files would roll the workloads they
# configure in the same apply as the pool change: flux.tf's
# local.app_stateful_scheduling and istio.tf's istiod pin both describe the
# capacity-type toleration as load-bearing and the on-demand pool as tainted.
# After this change the taint is gone, so those tolerations are inert rather than
# required, and on a spot-preferred env the istiod pin resolves to a spot-capable
# pool. Both are tracked in Lodestar-02z.7, the post-convergence cleanup.
#
# nodeClassRef below points at kubernetes_manifest.nodeclass_dozuki (our tagged
# class) instead of the built-in "default" one - see the comment on that resource
# above for why. nodeClassRef is a static-drift field just like the taint used to
# be: repointing it marks every EXISTING node in this pool Drifted, and Karpenter
# replaces them SERIALLY under this pool's own disruption budget (budget 1,
# consolidateAfter 5m, same as the taint removal above). Unlike that taint change
# there is no second pool being deleted underneath this one, so - on an env that
# has already converged past the two-pool collapse - this roll is serial only,
# with no concurrent whole-fleet cascade. Tags only land on nodes created AFTER
# this ships; every node that already exists keeps its untagged EC2 instance and
# EBS volume tags until it's replaced (by this drift roll or by ordinary
# consolidation/expiry) and a fresh CreateFleet call picks up the new class.
resource "kubernetes_manifest" "nodepool_on_demand" {
  count = var.cloud == "aws" ? 1 : 0

  # Ordering only - nodeClassRef.name below is a plain string, not a resource
  # reference, so there is no implicit dependency on the NodeClass existing
  # first. Without this, Karpenter could reconcile the NodePool before the
  # NodeClass object it names is present.
  depends_on = [kubernetes_manifest.nodeclass_dozuki]

  manifest = {
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata = {
      name = "on-demand"
    }
    spec = {
      template = {
        spec = merge(
          {
            nodeClassRef = {
              group = "eks.amazonaws.com"
              kind  = "NodeClass"
              name  = "dozuki"
            }
            requirements = concat([
              {
                key      = "karpenter.sh/capacity-type"
                operator = "In"
                # on-demand: production. spot-preferred: spot with on-demand as
                # FALLBACK, never hard spot-only. Karpenter's
                # price-capacity-optimized allocation picks spot whenever any spot
                # offering exists, so steady-state cost is the spot price - but
                # when an AZ's spot pool empties (a real ICE event), pods get
                # on-demand capacity in that AZ instead of pending until AWS
                # restores spot. That matters most for a pod whose EBS volume pins
                # it to the dry AZ: under spot-only that was an unbounded outage
                # (the adversarial AZ/PVC review's top finding, and the same
                # terminal state as the old spot-fleet stranding incidents).
                values = var.capacity_profile == "spot-preferred" ? ["spot", "on-demand"] : ["on-demand"]
              },
              {
                key      = "kubernetes.io/arch"
                operator = "In"
                values   = ["amd64"]
              },
              # Exclude the hardware classes that are wrong for a general
              # workload pool, rather than whitelisting a fixed set of
              # families: without this, arch-only requirements let Karpenter
              # buy previous-generation burstable instances - a live smoke
              # cluster ran prometheus, alertmanager and opensearch on a
              # c4.xlarge, with t2.medium alongside (t-class CPU credits
              # throttle sustained load into mystery latency).
              #
              # node_excluded_instance_categories drops: t (burstable,
              # credit-throttled), p/g/gr/inf/trn/dl/vt/f (accelerators - GPU,
              # graphics, Inferentia, Trainium, FPGA and video-transcoding
              # variants - nothing here schedules onto them), hpc (HPC-tuned, tightly-coupled
              # networking this pool doesn't use), mac (Apple silicon, not
              # a general shape), u (ultra-high-memory, far past what any
              # workload here needs). Everything else - c, m, r, i, d, z, x
              # and future families alike - is eligible by default; there is
              # no floor on category beyond this exclusion. Karpenter's
              # price-capacity-optimized allocation then picks the cheapest
              # shape that clears the generation and memory/vCPU
              # requirements below, so a new family becomes usable
              # automatically instead of waiting on a whitelist edit.
              {
                key      = "eks.amazonaws.com/instance-category"
                operator = "NotIn"
                values   = var.node_excluded_instance_categories
              },
              {
                key      = "eks.amazonaws.com/instance-generation"
                operator = "Gt"
                values   = ["4"]
              },
              # Memory floor, on top of the category/generation one. This exists
              # for the workloads whose requests can never express what they
              # need, NOT to top up an under-request.
              #
              # Requests are the right tool when a pod simply asks for too
              # little, and opensearch is that case - it is sized in the chart
              # and lands where its request says. Prometheus is the case
              # requests cannot solve. It requests 1536Mi and backs a 50Gi TSDB,
              # so it fits a 4GiB shape honestly, by the numbers, and Karpenter
              # will keep bin-packing it there forever. What it actually needs is
              # the ~1.4GiB of headroom that is left over for page cache after
              # the request, plus the EBS baseline that comes with a larger
              # shape; neither is something a request can ask for. Raising its
              # request to reserve page cache would be lying about the working
              # set to get an instance size, which is how you end up with a
              # request nobody can explain two years from now.
              #
              # Alertmanager (200Mi) and grafana (nothing at all until the chart
              # fix) are the same shape of problem, smaller: correctly sized or
              # not, they get stacked onto whatever 4GiB node has nominal room.
              #
              # Units are mebibytes and Gt takes exactly one value. Gt is
              # EXCLUSIVE, so the default 4096 drops c*.large and makes m*.large
              # (8GiB) the smallest node - identical to the literal it replaced.
              # The sizing model raises this per env via node_min_memory_mib
              # (8192 admits 16GiB and up, which is what lets an env sit on three
              # xlarge nodes instead of six large ones).
              {
                key      = "eks.amazonaws.com/instance-memory"
                operator = "Gt"
                values   = [tostring(var.node_min_memory_mib)]
              }
              ],
              # Optional vCPU floor. Karpenter's Gt is exclusive here too, but note
              # this variable IS decremented while node_min_memory_mib above is
              # NOT: a memory floor of 8192 renders Gt "8192" and so admits 16GiB
              # upward, whereas node_min_vcpu of 4 renders Gt "3", i.e. at least
              # 4 vCPU. Omitted entirely at the
              # default of 0 so envs that have not been sized yet keep exactly
              # today's behaviour. Paired with the memory floor it selects the
              # node shape: >=4 vCPU and >8192 MiB admits m*.xlarge / r*.xlarge and
              # the 2xlarge sizes, and excludes c*.xlarge (4 vCPU but only 8GiB).
              var.node_min_vcpu > 0 ? [{
                key      = "eks.amazonaws.com/instance-cpu"
                operator = "Gt"
                values   = [tostring(var.node_min_vcpu - 1)]
              }] : []
            )
            # No taints. This pool was tainted
            # eks.amazonaws.com/capacity-type=on-demand:NoSchedule so that only
            # workloads that deliberately tolerated it landed here, with the spot
            # pool as the default landing zone. With one pool there is no default
            # to steer away from, so the taint would just be a toleration every
            # pod had to carry. Removing it is what lets the two pools' workloads
            # pack together onto one set of nodes.
            #
            # Taints are a static-drift field, so removing this marks every
            # EXISTING node in the pool as drifted and Karpenter replaces them
            # serially (budget 1, consolidateAfter 5m). That is a full node-fleet
            # replacement per env on first apply, not a cosmetic change.
            #
            # Two DIFFERENT mechanisms run on that first apply and only one of
            # them is serial - do not read the budget above as governing both:
            #   - this pool's taint drift: serial, budget 1, 5m cadence.
            #   - the retired spot pool: its NodePool object is DELETED, and
            #     Karpenter cascades that to every NodeClaim the pool owns AT
            #     ONCE. The disruption budget here is scoped to this pool and
            #     never applied to that one. So the former spot fleet drains
            #     concurrently (PDBs still honoured) while this pool replaces one
            #     node at a time.
            # Net effect per env: node count rises before it falls, because spot
            # refugees cannot land on still-tainted nodes and Karpenter must
            # provision new untainted ones. Roll one env at a time and check PDB
            # coverage for the app tier first.
          },
          # Fresh-node race: pods scheduled before istio-cni is ready silently
          # bypass the mesh (STRICT then rejects them). The taint blocks
          # scheduling; istiod's untaint controller (taint.enabled) removes it
          # per node once the CNI agent is ready. App pods can ONLY land on this
          # custom pool (physical enables just the built-in system pool, which is
          # CriticalAddonsOnly), so coverage is total.
          local.mesh_enabled ? {
            startupTaints = [{
              key    = "cni.istio.io/not-ready"
              effect = "NoSchedule"
            }]
          } : {}
        )
      }
      disruption = {
        # Was WhenEmpty pool-wide: this pool hosts the on-demand-pinned app tier
        # and (via the values CPI sets on the monitoring/search subcharts) every
        # EBS-backed singleton. Underutilization-driven bin-packing was detaching
        # and re-attaching 50Gi volumes on routine 60-second consolidation
        # passes, and every forced reschedule re-rolled the AZ-capacity dice for
        # zone-pinned PVCs - so the whole pool was exempted from bin-packing to
        # protect four pods.
        #
        # That protection now lives on the pods instead of the pool. The EBS-volume
        # owners carry `karpenter.sh/do-not-disrupt: "true"` via flux.tf's
        # local.do_not_disrupt_annotation: prometheus and alertmanager always,
        # opensearch only below 3 replicas (at 3+ it has replica redundancy and
        # flux.tf drops the annotation). A node hosting one of those pods is
        # still skipped by consolidation; every other node in the pool - the
        # ones with no such pod, sitting empty or near-empty - is repacked
        # normally. consolidateAfter stays 5m (the retired spot pool ran 1m) so a
        # pod that's mid-reschedule doesn't trigger a repack on the churn itself;
        # that 1m cadence was part of what made the first
        # WhenEmptyOrUnderutilized attempt on this pool noisy.
        #
        # Consequence worth stating plainly: a do-not-disrupt pod's node is
        # ineligible for VOLUNTARY consolidation for as long as that pod lives
        # there. Drift still forces through eventually, bounded by the NodeClaim's
        # terminationGracePeriod, and no expireAfter is set on this pool, so age
        # alone never recycles it. Of the forceful paths the annotation never
        # covered, only spot interruption is made irrelevant by this being the
        # on-demand pool; node repair and manual delete still apply.
        #
        # Resulting floor, measured 2026-08-21 and recorded on Lodestar-02z.21:
        # 1 node in the collapsed prod envs, where opensearch runs 3 replicas and
        # is therefore unpinned, leaving prometheus and alertmanager to share one
        # node; 2-3 where opensearch is unpinned and they do not co-locate (dev-min
        # at .large shapes sat them on separate nodes) or where a single-replica
        # opensearch adds a pin of its own. Consolidation can shrink everything
        # else on the pool, but it cannot repack the pinned pods onto fewer nodes.
        consolidationPolicy = "WhenEmptyOrUnderutilized"
        consolidateAfter    = "5m"
        # One node at a time regardless of pool size. The CRD default is 10% which
        # rounds up to 1 at today's 4-7 nodes/env, but that scales invisibly with
        # the pool; pin it so a repack wave can never take two nodes at once.
        budgets = [{ nodes = "1" }]
      }
      weight = 10
    }
  }
}

resource "helm_release" "external_secrets" {
  depends_on = [helm_release.cert_manager]

  name       = "external-secrets"
  namespace  = kubernetes_namespace_v1.app.metadata[0].name
  repository = "https://charts.external-secrets.io"
  chart      = "external-secrets"
  # Pin the chart: unpinned it floats upstream latest, so two stacks applied a
  # week apart run different ESO versions. Bump deliberately. The strict-mTLS
  # PeerAuthentication carve-out targets this chart's rendered webhook labels
  # and port; check those still match before bumping this version.
  version = "2.8.0"

  wait = true

  set = [
    {
      name  = "crds.createClusterExternalSecret"
      value = "true"
    },
    {
      name  = "crds.createClusterSecretStore"
      value = "true"
    },
    {
      # Chart default is 1. A single webhook replica is a single point of
      # failure for every ExternalSecret/SecretStore admission in the
      # cluster (fail-closed) - the dev-min rollback loop on 2026-08-01 was
      # made worse by exactly this: the sole webhook pod rescheduling took
      # down 6 ExternalSecrets + the SecretStore at once.
      name  = "webhook.replicaCount"
      value = "2"
    },
    {
      # Chart default is false. Without one, nothing stops Karpenter from
      # draining a node and taking every webhook replica on it at once,
      # which is the fail-closed admission outage we are fixing.
      name  = "webhook.podDisruptionBudget.enabled"
      value = "true"
    },
    {
      # maxUnavailable, not minAvailable: the chart's own PDB template
      # prefers maxUnavailable over minAvailable once both keys are present,
      # and minAvailable=2 on a 2-replica deployment would forbid every
      # eviction outright, permanently blocking Karpenter consolidation of
      # any node hosting a webhook replica.
      name  = "webhook.podDisruptionBudget.maxUnavailable"
      value = "1"
    },
    {
      # Chart ships no requests at all. Two consequences: the pod is free in
      # Karpenter's bin-packing, so it can be packed onto a node with no real
      # headroom left, and it lands in BestEffort QoS, which is what the kubelet
      # evicts first under node pressure. Small real requests fix both. They do
      # NOT affect the topology spread below - PodTopologySpread scoring counts
      # matching pods per domain and never looks at requests. Requests only, no
      # limits: an under-set memory limit OOMKills the webhook, which is the
      # exact fail-closed outage this block exists to prevent. Measured on
      # dev-min 2026-08-02: 1m CPU / 32Mi. Memory is requested at 64Mi rather
      # than the observed 32Mi to leave headroom and keep the pod out of the
      # front of the eviction queue.
      name  = "webhook.resources.requests.cpu"
      value = "10m"
    },
    {
      name  = "webhook.resources.requests.memory"
      value = "64Mi"
    },
    {
      # A Pending replica keeps the PDB blocked: disruptionsAllowed counts
      # healthy pods, not scheduled ones, so one unschedulable replica stalls
      # every drain behind it. Priority shortens that window, it does not remove
      # it. Preemption only fires when evicting lower-priority pods on some node
      # actually makes this pod fit, and at a 10m/64Mi request it usually fits
      # somewhere without preempting anything; on a genuinely full cluster it
      # still waits on Karpenter. What the class buys unconditionally is the
      # rest: head of the scheduling queue, nothing else can preempt it, and the
      # kubelet ranks it last for node-pressure eviction. Worth it for a
      # fail-closed admission webhook, where every ExternalSecret and SecretStore
      # in the cluster is stuck behind it. Built-in class, works outside
      # kube-system, already in use on dev-min (dozuki, istio-system,
      # amazon-cloudwatch).
      name  = "webhook.priorityClassName"
      value = "system-cluster-critical"
    },
  ]

  # List-of-objects value, safer as a values block than a dotted/indexed
  # `set` entry. Biases the 2 webhook replicas onto separate nodes so a
  # single node loss is less likely to take out both. Soft (ScheduleAnyway),
  # not DoNotSchedule: a hard constraint would strand the 2nd replica
  # Pending forever on genuinely single-node clusters (the smallest
  # CloudPrem tiers). Soft means it is a preference, not a guarantee: on a
  # cluster with one schedulable node both replicas still land together, and
  # we can lose the pair. topologyKey is hostname, so this is node-loss
  # protection only and buys nothing against a zone failure. No labelSelector
  # needed - the chart auto-fills it from the webhook's own selector labels
  # when omitted.
  values = [
    yamlencode({
      webhook = {
        topologySpreadConstraints = [
          {
            maxSkew           = 1
            topologyKey       = "kubernetes.io/hostname"
            whenUnsatisfiable = "ScheduleAnyway"
          }
        ]
      }
    })
  ]
}

# Service account for ESO to authenticate to Vault via K8s auth.
# The SecretStore template references this SA by name.
resource "kubernetes_service_account_v1" "eso_vault_auth" {
  metadata {
    name      = "dozuki-external-secrets"
    namespace = kubernetes_namespace_v1.app.metadata[0].name

    annotations = var.cloud == "azure" ? {
      "azure.workload.identity/client-id" = var.azure_eso_identity_client_id
    } : {}

    labels = var.cloud == "azure" ? {
      "azure.workload.identity/use" = "true"
    } : {}
  }
}

# Image pull secret for GHCR (Azure only) — MPC images are pulled directly
# from ghcr.io instead of being mirrored into ACR.
resource "kubernetes_secret_v1" "ghcr_pull" {
  count = var.cloud == "azure" ? 1 : 0

  metadata {
    name      = "ghcr-pull"
    namespace = kubernetes_namespace_v1.app.metadata[0].name
  }

  type = "kubernetes.io/dockerconfigjson"

  data = {
    ".dockerconfigjson" = jsonencode({
      auths = {
        "ghcr.io" = {
          auth = base64encode("${var.ghcr_pull_username}:${var.ghcr_pull_token}")
        }
      }
    })
  }
}

# CloudWatch Observability add-on — installed in logical (not physical) because
# on EKS Auto Mode a fresh cluster has zero nodes until a workload is scheduled;
# cert-manager triggers node creation, so this addon installs after nodes exist
# (in physical it would sit DEGRADED with no nodes and time out). The IAM role +
# pod-identity association for the cloudwatch-agent SA live in the physical layer.
#
# We deliberately do NOT pre-delete a pre-existing copy: this addon is
# terraform-managed here, so deleting it out-of-band turns an in-place update into
# a failed modify against a just-deleted addon (ListTagsForResource 404).
# resolve_conflicts_on_create=OVERWRITE handles field-level conflicts on adoption.
resource "aws_eks_addon" "cloudwatch_observability" {
  count        = var.cloud == "aws" ? 1 : 0
  cluster_name = data.aws_eks_cluster.main[0].name
  addon_name   = "amazon-cloudwatch-observability"

  depends_on = [helm_release.cert_manager]

  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "OVERWRITE"

  # Application Signals "Auto-monitor" defaults ON since addon v5.0.0: the
  # bundled OTel operator webhook auto-injects ADOT SDKs + instrumentation
  # annotations into every workload (the reason ratelimit.tf ignores template
  # annotations, and why web-nextjs/grafana pods carry an ADOT SDK nobody
  # asked for), and the agent exports the resulting traces to X-Ray, which
  # nothing reads (no dashboards, alarms, SLOs, or custom groups in any
  # account). APM is Datadog's job (datadog.tf), so auto-monitor goes off.
  # Container Insights metrics and Fluent Bit log collection are independent
  # of this key and keep working. This is the only opt-out valid on BOTH the
  # 5.x and 6.x addon schemas (the fleet is mixed); once every stack is on
  # 6.x, add {"applicationSignals":{"enabled":false}} to also strip the
  # app-signals pipelines from the agent config. Already-injected pods keep
  # their ADOT SDK until restarted.
  #
  # This replaces the per-workload autoMonitor.exclude map that briefly lived
  # here: injected init containers (one per enabled language, each with
  # requests) made the node-affinity-pinned prometheus-node-exporter daemonset
  # unschedulable and stalled every deploy for helm's full 4h30m wait, and the
  # frontegg Node 12 images crashlooped on the injected SDK's optional
  # chaining. No injection anywhere is a superset of those excludes.
  # This addon is now a LOG SHIPPER ONLY. Fluent Bit stays and keeps writing the
  # application / dataplane / host log groups; the CloudWatch metrics agent is
  # gone. Metrics are Prometheus' job now (kube-prometheus-stack scrapes and
  # remote-writes to Mimir), so Container Insights was a second collector
  # publishing a strictly smaller set of the same signals, and it cost 250m CPU
  # of every node's 1780m allocatable to do it. The node_* alarms it fed were
  # deleted with it (physical/monitoring.tf); node-exporter's rules already
  # covered those three signals and about thirty more.
  #
  # `agents = []` rather than `containerInsights.enabled = false` ON PURPOSE.
  # The fleet is on mixed addon schemas (5.4.0 through 6.4.0 as of this change),
  # every schema sets additionalProperties=false, and `containerInsights` does
  # not exist before 6.x - setting it fails the addon update outright on any
  # stack still on 5.x. `agents` is an unconstrained array in every schema from
  # 5.4.0 on, so an empty list is portable and drops the daemonset the same way.
  # Revisit if addon_version ever gets pinned fleet-wide.
  #
  # Fluent Bit's memory request goes UP, not down. The addon default is 25Mi,
  # which is below what it actually uses: measured over 3 days on the busiest
  # commercial MPC env, median 44Mi, second-highest pod 58Mi, peak 68Mi. A
  # daemonset requesting less than it uses is ranked first for eviction under
  # node memory pressure, which is precisely backwards for the one component
  # this addon still exists to run. 96Mi sits ~1.4x over the measured peak. CPU
  # stays at the default 50m (measured median 6m, peak 21m).
  #
  # applicationSignals auto-monitor stays off. It is the bundled OTel operator
  # webhook, not the agent, so removing the agent does not stop it injecting ADOT
  # SDK init containers into every workload (that is why ratelimit.tf ignores
  # template annotations, and why a Go node-exporter was carrying a JVM agent).
  # This key is valid on both 5.x and 6.x; `applicationSignals.enabled` is 6.x
  # only, so it stays out until the fleet's schemas converge.
  configuration_values = jsonencode({
    agents = []
    containerLogs = {
      enabled = true
      fluentBit = {
        # DaemonSet, so this request is charged on every node in the fleet and the
        # trim multiplies by node count. cpu from 14d measured usage (p95 23.6m);
        # memory left at 96Mi, which is only a hair above the 72Mi max working set.
        resources = {
          requests = {
            cpu    = "40m"
            memory = "96Mi"
          }
          limits = {
            cpu    = "500m"
            memory = "250Mi"
          }
        }
      }
    }
    manager = {
      # The operator reconciles a handful of CRs and then idles: p95 7.7m of CPU over
      # 14d across 9 envs against the addon default of 100m. Memory left alone - its
      # 58Mi working set is close enough to the 64Mi default to leave standing.
      resources = {
        requests = {
          cpu = "15m"
        }
      }
      applicationSignals = {
        autoMonitor = {
          monitorAllServices = false
        }
      }
    }
  })

  tags = merge(
    {},
    var.delete_after != "" ? { deleteAfter = var.delete_after } : {},
  )

  # Headroom for the first node's Karpenter cold-start on a brand-new cluster.
  timeouts {
    create = "40m"
    update = "40m"
  }
}

locals {
  # Memcached is always in-cluster, on every cloud. The ElastiCache alternative is gone.
  # The app's Hostname type rejects a bare single-label name ("Invalid hostname"), so this
  # must be the service FQDN. This single source feeds the chart value, the config-map
  # values, AND the Vault-seeded cache secret (ESO-synced into memcached.json, which
  # OVERRIDES the chart config map) — all three must agree or the app reads an
  # empty/invalid host.
  memcached_host = "dozuki-memcached.${local.k8s_namespace_name}.svc.cluster.local"
}

# Moved blocks: these resources gained `count` when Azure support was added.
# They keep existing AWS state addresses from churning.
moved {
  from = kubernetes_storage_class_v1.ebs_gp3
  to   = kubernetes_storage_class_v1.ebs_gp3[0]
}


moved {
  from = kubernetes_manifest.tgb_https
  to   = kubernetes_manifest.tgb_https[0]
}

moved {
  from = kubernetes_manifest.tgb_http
  to   = kubernetes_manifest.tgb_http[0]
}

moved {
  from = kubernetes_manifest.nodepool_on_demand
  to   = kubernetes_manifest.nodepool_on_demand[0]
}

moved {
  from = aws_eks_addon.cloudwatch_observability
  to   = aws_eks_addon.cloudwatch_observability[0]
}
