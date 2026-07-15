# ---------------------------------------------------------------------------
# Datadog agent: APM + Continuous Profiler for the monolith (MPC-internal)
# ---------------------------------------------------------------------------
#
# Gated hard off by default: this is Dozuki-internal observability, never part
# of the CloudPrem customer product. Only MPC stacks flip enable_datadog, and
# only on AWS (the flag is folded with var.cloud below so an Azure stack that
# sets it gets nothing rather than a broken aws_eks_cluster index).
#
# Deliberately lean install. Log aggregation and container metrics stay on
# CloudWatch/Container Insights; Datadog's job here is trace intake, the
# admission-controller library injection (Single Step Instrumentation), and
# the continuous profiler. Everything else the chart enables by default is
# flipped off so we don't ship (and pay for) a second copy of telemetry we
# already collect. The node agent daemonset itself cannot be disabled - every
# node bills as a Datadog infra host; that is the floor for SSI.
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

  data = {
    "api-key" = data.vault_kv_secret_v2.datadog[0].data["api_key"]
  }
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
    # Declarative SSI works with Remote Config off; RC only matters for
    # UI/Fleet-Automation-driven enablement, which we don't want competing
    # with terraform.
    remoteConfiguration = {
      enabled = false
    }

    datadog = {
      site                 = local.datadog_site
      apiKeyExistingSecret = kubernetes_secret_v1.datadog_api_key[0].metadata[0].name

      # REQUIRED on EKS Auto Mode: pods can't reach IMDS (hop limit locked to
      # 1), so cluster-name autodetection fails in the cluster agent.
      clusterName = data.aws_eks_cluster.main[0].name

      tags = ["env:${local.datadog_env}"]

      # ---- lean: flip the chart defaults that are on ----
      collectEvents = false
      kubeStateMetricsCore = {
        enabled = false
      }
      clusterChecks = {
        enabled = false
      }
      orchestratorExplorer = {
        enabled = false
      }
      processAgent = {
        processCollection   = false
        processDiscovery    = false
        containerCollection = false
      }
      # Already off by default; pinned so a chart bump can't silently start
      # double-shipping logs we keep in CloudWatch.
      logs = {
        enabled             = false
        containerCollectAll = false
      }
      networkMonitoring = {
        enabled = false
      }
      serviceMonitoring = {
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
      admissionController = {
        enabled = true
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
