# ---------------------------------------------------------------------------
# Datadog agent: APM + Continuous Profiler for the monolith (MPC-internal)
# ---------------------------------------------------------------------------
#
# Gated hard off by default: this is Dozuki-internal observability, never part
# of the CloudPrem customer product. Only MPC stacks flip enable_datadog, and
# only on AWS (the flag is folded with var.cloud below so an Azure stack that
# sets it gets nothing rather than a broken aws_eks_cluster index).
#
# Lean core plus three opt-in tiers. The core install (enable_datadog alone)
# is trace intake, the admission-controller library injection (Single Step
# Instrumentation), and the continuous profiler - everything else the chart
# enables by default is flipped off so we don't ship (and pay for) a second
# copy of telemetry we already collect. The node agent daemonset itself cannot
# be disabled - every node bills as a Datadog infra host; that is the floor
# for SSI.
#
# Each tier is a separate bool, all default false, all requiring
# enable_datadog:
#
#   enable_datadog_logs   log collection (containerCollectAll). Dual-ships
#                         with CloudWatch/Container Insights on purpose until
#                         a migration decision is made - do not read this as
#                         "logs moved to Datadog".
#   enable_datadog_infra  kube-state-metrics core, cluster checks, the
#                         orchestrator explorer, process/container collection
#                         and Kubernetes event collection.
#   enable_datadog_dpa    Remote Configuration + the workload autoscaling
#                         controller and its CRD, plus the app key. Installs
#                         the machinery only; creating a DatadogPodAutoscaler
#                         is a separate deliberate act.
#
# The tiers are being cost-measured on one env at a time. Turning one on
# fleet-wide is a decision that needs the measured numbers, not a default.
#
# SSI scoping: when instrumentation.targets is non-empty the cluster agent
# ONLY injects pods matching a target - there is no instrument-everything
# fallback (that default is synthesized only when targets is empty). So the
# PHP monolith pods listed below get dd-trace-php and nothing else in the
# cluster is touched: web-nextjs keeps its Sentry tracing untouched and the
# subchart workloads (opensearch, seaweedfs, grafana, ...) stay clean.
# Targets match first-wins and configs do NOT merge across targets, which is
# why each target repeats the shared DD_* config.
#
# The chart must NOT install into the app namespace: SSI never instruments
# the agent's own namespace, so it gets a dedicated "datadog" one.

locals {
  # enable_datadog is the per-stack switch; AWS-only for now (agent works on
  # AKS but nothing Azure-side is wired or tested).
  datadog_enabled = var.enable_datadog && var.cloud == "aws"

  datadog_chart_version = "3.231.2" # agent + cluster agent 7.80.1
  datadog_site          = "datadoghq.com"

  # env: is the primary APM dimension; customer-environment matches how we
  # name stacks everywhere else (dev-min, ...).
  datadog_env = "${var.customer}-${var.environment}"

  # Shared per-target tracer config. Profiling "auto" is Datadog's
  # recommended value under SSI (profiles only eligible processes).
  datadog_php_base_configs = [
    { name = "DD_ENV", value = local.datadog_env },
    { name = "DD_PROFILING_ENABLED", value = "auto" },
  ]
}

# API key lives at secret/dozuki/global/datadog (field api_key), populated
# out-of-band with `vault kv put` on 2026-07-15. There is deliberately NO
# vault-config placeholder resource for it: creating vault_kv_secret_v2 over
# an already-populated path writes an empty version on top (ignore_changes
# only protects existing state, not creation).
data "vault_kv_secret_v2" "datadog" {
  count = local.datadog_enabled ? 1 : 0

  mount = "secret"
  name  = "dozuki/global/datadog"
}

resource "kubernetes_namespace_v1" "datadog" {
  count = local.datadog_enabled ? 1 : 0

  metadata {
    name = "datadog"
  }
}

# The chart reads the key from an existing secret (key name "api-key" is the
# chart's contract). Terraform-managed rather than ESO: the agent is infra
# tooling scoped to this stack, and the secret must exist before the release
# installs (agent pods mount it at start), which rules out the
# create_namespace + post-release-secret pattern envoy-gateway uses.
resource "kubernetes_secret_v1" "datadog_api_key" {
  count = local.datadog_enabled ? 1 : 0

  metadata {
    name      = "datadog-api-key"
    namespace = kubernetes_namespace_v1.datadog[0].metadata[0].name
  }

  # app-key rides in the same secret only when the workload autoscaling tier is
  # on: the cluster agent authenticates its recommendation calls with an app
  # key, and datadog.appKeyExistingSecret's contract is the "app-key" field of
  # the secret already named by apiKeyExistingSecret.
  data = merge(
    {
      "api-key" = data.vault_kv_secret_v2.datadog[0].data["api_key"]
    },
    var.enable_datadog_dpa ? {
      "app-key" = data.vault_kv_secret_v2.datadog[0].data["app_key"]
    } : {},
  )
}

resource "helm_release" "datadog" {
  count = local.datadog_enabled ? 1 : 0

  name       = "datadog"
  namespace  = kubernetes_namespace_v1.datadog[0].metadata[0].name
  repository = "https://helm.datadoghq.com"
  chart      = "datadog"
  version    = local.datadog_chart_version

  # Same Auto Mode headroom as envoy-gateway: the cluster-agent Deployment can
  # itself be what triggers node provisioning, and 300s is too tight for a
  # Karpenter cold start.
  timeout = 600
  wait    = true

  values = [yamlencode({
    # Declarative SSI works with Remote Config off, and off is still the
    # default here. RC goes on only with the workload autoscaling tier, which
    # requires it: the cluster agent receives its scaling recommendations over
    # the RC channel, so there is no DPA without it.
    #
    # Terraform remains the source of truth for agent CONFIG. RC being on
    # opens a second write path (Datadog UI / Fleet Automation) that Terraform
    # does not see. RC traffic belonging to the autoscaling feature's own data
    # plane is expected; any RC-originated change to logs, APM, metrics or
    # feature config is config drift and gets escalated, not absorbed.
    remoteConfiguration = {
      enabled = var.enable_datadog_dpa
    }

    datadog = {
      site                 = local.datadog_site
      apiKeyExistingSecret = kubernetes_secret_v1.datadog_api_key[0].metadata[0].name

      # REQUIRED on EKS Auto Mode: pods can't reach IMDS (hop limit locked to
      # 1), so cluster-name autodetection fails in the cluster agent.
      clusterName = data.aws_eks_cluster.main[0].name

      tags = ["env:${local.datadog_env}"]

      # The app key is only mounted for the workload autoscaling tier. Empty
      # string is the chart default and is falsy in its templates, so the
      # tier-off render is unchanged.
      appKeyExistingSecret = var.enable_datadog_dpa ? kubernetes_secret_v1.datadog_api_key[0].metadata[0].name : ""

      # Gates the datadog-crds subchart (DatadogPodAutoscaler) and the cluster
      # agent's workload autoscaling controller. Needs remoteConfiguration on,
      # above. NOT the datadog-operator: the CRD ships with the crds subchart
      # and the controller lives in the cluster agent, so operator stays off.
      autoscaling = {
        workload = {
          enabled = var.enable_datadog_dpa
        }
      }

      # ---- tiers: off by default, same render as the lean install ----
      collectEvents = var.enable_datadog_infra
      kubeStateMetricsCore = {
        enabled = var.enable_datadog_infra
      }
      clusterChecks = {
        enabled = var.enable_datadog_infra
      }
      orchestratorExplorer = {
        enabled = var.enable_datadog_infra
      }
      processAgent = {
        processCollection   = var.enable_datadog_infra
        processDiscovery    = var.enable_datadog_infra
        containerCollection = var.enable_datadog_infra
      }
      # Off by default and pinned so a chart bump can't silently start
      # double-shipping logs we keep in CloudWatch. When the tier is on the
      # dual-ship is deliberate and time-boxed, not a migration.
      logs = {
        enabled             = var.enable_datadog_logs
        containerCollectAll = var.enable_datadog_logs
      }
      networkMonitoring = {
        enabled = false
      }
      serviceMonitoring = {
        enabled = false
      }
      # agent >= 7.78 auto-enables service discovery (spawns system-probe);
      # a lean install wants neither the extra privileged container nor the
      # noise. The chart pins 7.80.1, so this is on unless we say otherwise.
      discovery = {
        enabled = false
      }

      # The chart bundles datadog-operator as a subchart, on by default. We
      # install the agent with helm directly and don't want a second
      # controller reconciling it. This is the key that actually gates the
      # subchart (its Chart.yaml condition); the top-level operator.* keys
      # only toggle controllers INSIDE the operator and leave the Deployment
      # running.
      operator = {
        enabled = false
      }
      sbom = {
        containerImage = { enabled = false }
        host           = { enabled = false }
      }

      apm = {
        socketEnabled = true  # trace + profile intake over UDS
        portEnabled   = false # no hostPort 8126
        instrumentation = {
          enabled = true
          language_detection = {
            enabled = false # php is pinned explicitly per target
          }
          # queueworker first: first match wins, and it needs the two extra
          # long-running-CLI settings (one trace per beanstalkd job instead
          # of one unbounded never-flushed trace per worker process).
          targets = [
            {
              name              = "dozuki-queueworker"
              namespaceSelector = { matchNames = [local.k8s_namespace_name] }
              podSelector       = { matchLabels = { app = "queueworker" } }
              ddTraceVersions   = { php = "1" }
              ddTraceConfigs = concat(local.datadog_php_base_configs, [
                { name = "DD_SERVICE", value = "dozuki-queueworker" },
                { name = "DD_TRACE_GENERATE_ROOT_SPAN", value = "0" },
                { name = "DD_TRACE_AUTO_FLUSH_ENABLED", value = "1" },
              ])
            },
            {
              name              = "dozuki-app"
              namespaceSelector = { matchNames = [local.k8s_namespace_name] }
              podSelector       = { matchLabels = { app = "app" } }
              ddTraceVersions   = { php = "1" }
              ddTraceConfigs = concat(local.datadog_php_base_configs, [
                { name = "DD_SERVICE", value = "dozuki-app" },
              ])
            },
            {
              name              = "dozuki-crond"
              namespaceSelector = { matchNames = [local.k8s_namespace_name] }
              podSelector       = { matchLabels = { app = "crond" } }
              ddTraceVersions   = { php = "1" }
              ddTraceConfigs = concat(local.datadog_php_base_configs, [
                { name = "DD_SERVICE", value = "dozuki-crond" },
              ])
            },
          ]
        }
      }
    }

    # Both default true and both required for SSI (the chart hard-fails
    # without them) - pinned so nobody "leans" them off later. The admission
    # webhook keeps failurePolicy Ignore: on Auto Mode scale-from-zero a pod
    # racing the webhook starts uninstrumented instead of being blocked.
    clusterAgent = {
      enabled = true
      # KSM core, the orchestrator explorer, cluster checks and the workload
      # autoscaling controller all run here, so the cluster agent needs real
      # headroom once any tier is on. Sized for the tiers-on case rather than
      # varying per tier: a request change is a pod roll, and one shape is
      # easier to reason about than four.
      resources = {
        requests = { cpu = "200m", memory = "512Mi" }
        limits   = { memory = "1Gi" }
      }
      admissionController = {
        enabled = true
        # Pinned rather than inherited from the chart default: the documented
        # rollback path (scale the cluster agent to 0, delete the webhook)
        # depends on a racing pod starting uninstrumented instead of being
        # blocked by an admission timeout.
        failurePolicy = "Ignore"
      }
    }

    # July 2026 live-test hardening (see PR #251 history).
    agents = {
      # Auto Mode spot nodes carry eks.amazonaws.com/capacity-type; app pods
      # schedule there, and an untolerated agent means silent trace drop.
      tolerations = [{ operator = "Exists" }]
      containers = {
        agent = {
          # No requests = starved CFS shares on packed nodes = startup-probe
          # kill loop (observed live 2026-07-15). Requests stay at the
          # measured-healthy lean figure so scheduling pressure is unchanged;
          # the limit doubles because log tailing and process collection are
          # the documented memory adders and 512Mi is where the lean agent
          # already sat.
          resources = {
            requests = { cpu = "100m", memory = "256Mi" }
            limits   = { memory = "1Gi" }
          }
          startupProbe = {
            failureThreshold = 24
          }
        }
        traceAgent = {
          # Prometheus scrapes were 21.8% of span volume in the APM pilot at
          # zero analytical value - every /metrics pull became a trace. Drop
          # them at the trace agent rather than paying to ingest and filter.
          env = [
            {
              name  = "DD_APM_IGNORE_RESOURCES"
              value = "GET /metrics"
            },
          ]
        }
      }
    }

    providers = {
      eks = {
        ec2 = {
          # Must stay false on Auto Mode: it hostPath-mounts cloud-init's
          # instance-id file, which doesn't exist on Bottlerocket nodes, and
          # agent pods fail to mount.
          useHostnameFromFile = false
        }
      }
    }
  })]

  depends_on = [
    kubernetes_secret_v1.datadog_api_key,
    # Nodes exist only after cert-manager forces the first provisioning on a
    # fresh Auto Mode cluster (same reason the cloudwatch addon waits).
    helm_release.cert_manager,
  ]
}
