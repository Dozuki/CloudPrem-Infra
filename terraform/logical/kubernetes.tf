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

  wait = true

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
    # every kill dropped the xDS stream so the whole data plane went down (took apac
    # down mid-cutover). A 500m request gives it guaranteed scheduling headroom so the
    # probe stays responsive; no CPU limit so reconciliation can burst. (The chart
    # does not expose the controller's probe timeouts, so we fix this via resources;
    # verify this value path against the pinned gateway-helm chart on version bumps.)
    deployment = {
      envoyGateway = {
        resources = {
          requests = {
            cpu    = "500m"
            memory = "256Mi"
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

resource "kubernetes_manifest" "nodepool_spot" {
  count = var.cloud == "aws" ? 1 : 0

  manifest = {
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata = {
      name = "spot"
    }
    spec = {
      template = {
        spec = merge(
          {
            nodeClassRef = {
              group = "eks.amazonaws.com"
              kind  = "NodeClass"
              name  = "default"
            }
            requirements = [
              {
                key      = "karpenter.sh/capacity-type"
                operator = "In"
                # Spot preferred, on-demand as fallback. Karpenter's
                # price-capacity-optimized allocation picks spot whenever any
                # spot offering exists, so steady-state cost is unchanged - but
                # when an AZ's spot pool empties (a real ICE event), pods on
                # this pool get on-demand capacity there instead of pending
                # until AWS restores spot to that zone. Matters most for a pod
                # whose EBS volume pins it to the dry AZ: with spot-only this
                # was an unbounded outage (the adversarial AZ/PVC review's top
                # finding, and the same terminal state as the old spot-fleet
                # stranding incidents).
                values = ["spot", "on-demand"]
              },
              {
                key      = "kubernetes.io/arch"
                operator = "In"
                values   = ["amd64"]
              },
              # Floor the hardware: without these, arch-only requirements let
              # Karpenter buy previous-generation burstable spot - a live smoke
              # cluster ran prometheus, alertmanager and opensearch on a
              # c4.xlarge, with t2.medium alongside (t-class CPU credits
              # throttle sustained load into mystery latency). Mirrors the
              # built-in system pool's own category/generation floor.
              {
                key      = "eks.amazonaws.com/instance-category"
                operator = "In"
                values   = ["c", "m", "r"]
              },
              {
                key      = "eks.amazonaws.com/instance-generation"
                operator = "Gt"
                values   = ["4"]
              }
            ]
          },
          # Fresh-node race: pods scheduled before istio-cni is ready silently
          # bypass the mesh (STRICT then rejects them). The taint blocks
          # scheduling; istiod's untaint controller (taint.enabled) removes it
          # per node once the CNI agent is ready. App pods can ONLY land on
          # these custom pools (physical enables just the built-in system pool,
          # which is CriticalAddonsOnly), so coverage is total.
          local.mesh_installed ? {
            startupTaints = [{
              key    = "cni.istio.io/not-ready"
              effect = "NoSchedule"
            }]
          } : {}
        )
      }
      disruption = {
        consolidationPolicy = "WhenEmptyOrUnderutilized"
        consolidateAfter    = "1m"
      }
      weight = 100
    }
  }
}

resource "kubernetes_manifest" "nodepool_on_demand" {
  count = var.cloud == "aws" ? 1 : 0

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
              name  = "default"
            }
            requirements = [
              {
                key      = "karpenter.sh/capacity-type"
                operator = "In"
                values   = ["on-demand"]
              },
              {
                key      = "kubernetes.io/arch"
                operator = "In"
                values   = ["amd64"]
              },
              # Same hardware floor as the spot pool (see the comment there).
              {
                key      = "eks.amazonaws.com/instance-category"
                operator = "In"
                values   = ["c", "m", "r"]
              },
              {
                key      = "eks.amazonaws.com/instance-generation"
                operator = "Gt"
                values   = ["4"]
              },
              # Memory floor, on top of the category/generation one. Everything
              # pinned here (the app tier, and via app_stateful_scheduling in
              # flux.tf: opensearch, prometheus, alertmanager, grafana) wants a
              # node bigger than 4GiB, but nothing was enforcing it. A 4GiB shape
              # leaves ~3106Mi allocatable and opensearch requests 3Gi (3072Mi),
              # so it FITS - it stayed off c5a.large only because the AWS
              # DaemonSets (cloudwatch-agent 128Mi + fluent-bit 25Mi) ate the
              # difference, leaving a 119Mi accident that any addon-request
              # change in another repo could erase. Land there and opensearch
              # gets 800 baseline EBS IOPS and no page cache to speak of.
              # Units are mebibytes and Gt takes exactly one value, so >4096
              # drops c*.large and makes m*.large (8GiB) the smallest node.
              {
                key      = "eks.amazonaws.com/instance-memory"
                operator = "Gt"
                values   = ["4096"]
              }
            ]
            taints = [
              {
                key    = "eks.amazonaws.com/capacity-type"
                value  = "on-demand"
                effect = "NoSchedule"
              }
            ]
          },
          # See nodepool_spot for the startupTaints rationale.
          local.mesh_installed ? {
            startupTaints = [{
              key    = "cni.istio.io/not-ready"
              effect = "NoSchedule"
            }]
          } : {}
        )
      }
      disruption = {
        # WhenEmpty, not WhenEmptyOrUnderutilized: this pool exists for workloads
        # that are expensive to move - the on-demand-pinned app tier and (via the
        # values CPI sets on the monitoring/search subcharts) every EBS-backed
        # singleton. Underutilization-driven bin-packing was detaching and
        # re-attaching 50Gi volumes on routine 60-second consolidation passes,
        # and every forced reschedule re-rolls the AZ-capacity dice. These nodes
        # now go away only when empty, on AMI drift, or at the 336h expiry.
        consolidationPolicy = "WhenEmpty"
        consolidateAfter    = "1m"
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
  configuration_values = jsonencode({
    manager = {
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
  from = kubernetes_manifest.nodepool_spot
  to   = kubernetes_manifest.nodepool_spot[0]
}

moved {
  from = kubernetes_manifest.nodepool_on_demand
  to   = kubernetes_manifest.nodepool_on_demand[0]
}

moved {
  from = aws_eks_addon.cloudwatch_observability
  to   = aws_eks_addon.cloudwatch_observability[0]
}
