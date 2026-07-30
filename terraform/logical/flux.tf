# Flux app-delivery: the dozuki release is delivered by a Flux HelmRelease (helm-controller),
# not the Terraform helm provider. local.app_values is the nested, type-correct values map that
# replaces the old helm_release.app `set`/`set_sensitive` lists - built from the SAME vars/locals,
# never a `helm get values` snapshot (the snapshot emits unparseable YAML from the TLS bundles and
# silently falls back to chart defaults). Types mirror helm's strvals coercion: bare true/false ->
# bool, numeric -> number, the two redis.tls entries stay string (they were --set-string), all else
# string. A render/path diff against the retired set list is the correctness gate (see the flux
# validation harness). Values go into a Secret (values-Secret, create-not-apply for the 240KB size)
# referenced by the HelmRelease `valuesFrom`.

locals {
  # deployments.webNextjs.env came from two sources on helm_release.app: the values-block
  # (var.webnextjs_env) and appended set entries (var.nextjs_extra_env). Merge them (nextjs_extra_env
  # wins on key collisions, matching the old later-set-overrides-earlier-value order).
  app_webnextjs_env = merge(var.webnextjs_env, var.nextjs_extra_env)

  # gateway.hosts[0] is the primary; additional_gateway_hosts append from index 1.
  app_gateway_hosts = concat(
    [{ hostname = coalesce(var.ingress_hostname, var.dns_domain_name), tlsSecretName = "tls-secret" }],
    [for host in var.additional_gateway_hosts : { hostname = host.hostname, tlsSecretName = host.tls_secret_name }],
  )

  # Azure-only overrides (were the helm_release.app `values` yamlencode block). Built as a native map
  # that is EMPTY on non-azure via `for ... if` - a `cond ? {object} : {}` ternary fails because
  # Terraform won't unify a populated object with an empty one (the original only worked because it
  # was inside yamlencode() -> a string). try() guards keep the azure-only refs (lb_fqdn,
  # seaweedfs_values) from erroring when this is evaluated on aws/gov. Inner optional keys
  # (annotations, cert_manager) use the same `for ... if` idiom for the same reason.
  app_azure_values = {
    for k, v in merge(
      {
        global = {
          imagePullSecrets = [{ name = "ghcr-pull" }]
          seaweedfs        = { enableReplication = true, replicationPlacement = "001" }
        }
        gateway = {
          service = merge(
            { type = "LoadBalancer" },
            { for ak, av in { annotations = { "service.beta.kubernetes.io/azure-dns-label-name" = var.gateway_dns_label } } : ak => av if var.gateway_dns_label != "" },
          )
          dnsTarget = try(local.lb_fqdn, "")
        }
      },
      try(local.seaweedfs_values, {}),
      { for ck, cv in { cert_manager = { acmeServer = var.azure_acme_server } } : ck => cv if var.azure_acme_server != "" },
    ) : k => v if var.cloud == "azure"
  }

  # The base values, mirroring the old set + set_sensitive lists with correct types.
  app_base_values = {
    hostname = var.dns_domain_name
    dns_validation = (
      var.cloud == "aws" && !local.is_us_gov && !local.tls_manual &&
      contains(["dozuki.cloud", "dozuki.com", "dozuki.app", "dozuki.guide"], replace(var.dns_domain_name, "/^[^.]+\\./", ""))
    )
    customer    = coalesce(var.customer, "Dozuki")
    environment = var.environment

    aws = {
      region    = var.cloud == "aws" ? data.aws_region.current[0].region : "us-east-1"
      accountId = var.cloud == "aws" ? data.aws_caller_identity.current[0].account_id : ""
      enabled   = var.cloud == "aws"
    }

    db = {
      host       = local.db_master_host
      user       = local.db_master_username
      resourceId = var.db_resource_id
      rdsCaCert  = base64encode(file(local.ca_cert_pem_file))
      password   = local.db_master_password # (was set_sensitive)
    }
    dbMigrations = { activeDeadlineSeconds = var.db_migrations_active_deadline_seconds } # number

    smtp = {
      enabled = var.smtp_enabled
      host    = var.smtp_host
      from    = var.smtp_from_address
      auth = {
        enabled  = var.smtp_auth_enabled
        username = var.smtp_username
        password = var.smtp_password # (was set_sensitive)
      }
    }

    sentry = { customerName = coalesce(var.customer, "Dozuki") }

    images = {
      app       = { repository = var.image_repository, tag = var.image_tag }
      webnextjs = { tag = var.nextjs_tag }
    }

    ingress = { hosts = [{ hostname = coalesce(var.ingress_hostname, var.dns_domain_name) }] }

    gateway = {
      hosts    = local.app_gateway_hosts
      clientIP = { mode = var.cloud == "azure" ? "none" : "proxyProtocol" }
      stableProxyService = {
        enabled = contains(["aws", "azure"], var.cloud)
        targetGroupBindings = {
          httpsArn = var.cloud == "aws" ? var.nlb_https_target_group_arn : ""
          httpArn  = var.cloud == "aws" ? var.nlb_http_target_group_arn : ""
        }
      }
    }

    tls = {
      enabled             = local.tls_manual
      externallyManaged   = local.tls_externally_managed
      cert                = local.tls_chart_rendered ? var.tls_cert : ""
      key                 = local.tls_chart_rendered ? var.tls_key : "" # (was set_sensitive)
      vaultExternalSecret = { enabled = local.tls_from_vault }
    }


    objectStorage = {
      kmsKey          = var.cloud == "aws" ? data.aws_kms_key.s3[0].arn : ""
      imagesBucket    = var.cloud == "azure" ? local.seaweedfs_images_bucket : var.s3_images_bucket
      pdfsBucket      = var.cloud == "azure" ? local.seaweedfs_pdfs_bucket : var.s3_pdfs_bucket
      documentsBucket = var.cloud == "azure" ? local.seaweedfs_documents_bucket : var.s3_documents_bucket
      objectsBucket   = var.cloud == "azure" ? local.seaweedfs_objects_bucket : var.s3_objects_bucket
      endpoint        = var.cloud == "azure" ? local.seaweedfs_s3_endpoint : ""
      publicHost      = var.cloud == "azure" ? "s3.${var.dns_domain_name}" : ""
      credentials = {
        accessKey = var.cloud == "azure" ? try(random_password.seaweedfs_access_key[0].result, "") : "" # (was set_sensitive)
        secretKey = var.cloud == "azure" ? try(random_password.seaweedfs_secret_key[0].result, "") : "" # (was set_sensitive)
      }
    }

    # proxy.* is always emitted, even when off. The chart derives nil-safe locals
    # from this map, and an env whose stored values predate it would otherwise
    # render nothing for the key at all.
    memcached = {
      host          = local.memcached_host
      enabled       = true
      asciiProtocol = var.memcached_ascii_protocol
      proxy = {
        deploy          = var.memcached_proxy_deploy
        enabled         = var.memcached_proxy_enabled
        backendReplicas = var.memcached_proxy_backend_replicas
      }
    }

    vault = { enabled = var.cloud == "aws", address = var.vault_address }

    azure = {
      enabled     = var.cloud == "azure"
      tenantId    = var.azure_tenant_id
      keyVaultUri = var.azure_key_vault_uri
      environment = var.azure_environment
    }

    monitoring = {
      enabled = true
      # AWS/Gov stacks already expose the fleet-wide incoming webhook at
      # secret/dozuki/global/slack -> webhookUrl. The chart's ExternalSecret
      # projects it directly for Alertmanager; no third webhook value is copied
      # through Terraform or Helm. Azure has no Vault path, so stays disabled
      # until the same value is deliberately mirrored into Key Vault.
      alertmanager = {
        slack = {
          enabled = var.cloud == "aws"
          # The shared Slack app sends Silence 2h button callbacks to the
          # standalone interaction handler, which reuses the fleet-global ops
          # login. Keep the button off until that signature-verified handler is
          # deployed and configured in Slack.
          interactivity = {
            enabled = var.cloud == "aws" && var.alertmanager_slack_interactivity_enabled
          }
        }
      }
    }

    # metrics-server ships in the chart (default on) as the single source of truth across
    # onprem+cloud; the EKS addon was retired (#297). args=[] drops the chart's onprem-oriented
    # --kubelet-insecure-tls default (cloud kubelets present proper serving certs), keeping the
    # subchart's secure defaultArgs.
    "metrics-server" = { args = [] }

    dashboards = {
      enabled   = var.enable_dashboards
      jwtSecret = local.dashboards_jwt_secret # (was set_sensitive)
    }

    "dozuki-operator" = {
      image            = { repository = "${var.image_repository}/dozuki-operator" }
      imagePullSecrets = [{ name = "ghcr-pull" }]
      grafana          = { url = var.enable_dashboards ? "http://dozuki-dashboards-grafana" : "" }
      gatewayAPI       = { enabled = var.subsite_gateway_api_enabled }
    }

    grafana = {
      env = {
        GF_DATABASE_TYPE = var.enable_dashboards ? "mysql" : ""
        GF_DATABASE_HOST = var.enable_dashboards ? "${local.db_master_host}:3306" : ""
        GF_DATABASE_NAME = var.enable_dashboards ? "grafana_primary" : ""
        GF_DATABASE_USER = var.enable_dashboards ? local.db_master_username : ""
        # RDS enforces require_secure_transport=ON, so grafana's backend MySQL connection must be
        # TLS or it is refused (Error 3159, crashloops the dashboards-grafana pod and wedges the
        # helm upgrade). skip-verify encrypts without pinning the RDS CA. grafana's makeCert reads
        # ca_cert_path unconditionally even for skip-verify, so it needs a readable PEM: point it at
        # the CA bundle already in the grafana image (skip-verify ignores its contents, so it just
        # has to exist). The app's own primary DB path keeps full CA verification separately.
        GF_DATABASE_SSL_MODE     = var.enable_dashboards ? "skip-verify" : ""
        GF_DATABASE_CA_CERT_PATH = var.enable_dashboards ? "/etc/ssl/certs/ca-certificates.crt" : ""
      }
      envValueFrom = {
        GF_DATABASE_PASSWORD = { secretKeyRef = {
          name = var.enable_dashboards ? "grafana-db-credentials" : ""
          key  = var.enable_dashboards ? "password" : ""
        } }
      }
    }

    googleTranslate = { token = var.google_translate_api_token } # (was set_sensitive)

    deployments = { webNextjs = { env = local.app_webnextjs_env } }
  }

  # Final values = base, merged with the azure-only block. Two keys collide between the two and both
  # need a one-level-deeper merge (a shallow spread would replace base's whole subtree):
  #   - gateway: base sets hosts/clientIP/stableProxyService; azure adds service/dnsTarget.
  #   - objectStorage: base sets kmsKey/buckets/endpoint/credentials (the old set list); azure's
  #     seaweedfs_values adds publicBackend. Without the deep merge azure would keep only publicBackend
  #     and drop the buckets/credentials the app needs.
  # Every other azure key (global, cert_manager, seaweedfs...) has no base collision and shallow-merges
  # cleanly. helm_release.app got the same effect from helm's deep merge of its two values files + set
  # list. Both deep-merges are unconditional (no cond?{}:{} ternary): on non-azure app_azure_values has
  # neither key, so try(...,{}) yields {} and the merged result equals base.
  # AWS-only: pin every EBS-backed subchart workload to the on-demand NodePool.
  #
  # These pods (opensearch, prometheus, alertmanager, dashboards-grafana) each own a
  # zonal EBS volume. On the spot pool, an AZ-wide spot shortage left them Pending with
  # no legal fallback - the volume pins the zone, the on-demand pool's taint blocked
  # entry, and that is the old spot-fleet stranding incident reproduced (adversarial
  # AZ/PVC review, 2026-07-28; observed live: all three volumes on one spot c4.xlarge).
  # On-demand placement plus the on-demand pool's WhenEmpty consolidation makes their
  # disruptions rare and their replacement capacity reliably provisionable in the
  # volume's AZ.
  #
  # Lives HERE and not in chart defaults on purpose: the selector/toleration reference
  # Karpenter/EKS labels that do not exist on onprem or AKS clusters - baked into the
  # chart they would make these pods unschedulable there. Same reason the chart's
  # dozuki.onDemand* helpers gate on aws.enabled. The `for..if` idiom (not cond?{}:{})
  # matches app_azure_values - see the type-unification note above it.
  stateful_node_selector = { "karpenter.sh/capacity-type" = "on-demand" }
  stateful_tolerations = [{
    key      = "eks.amazonaws.com/capacity-type"
    operator = "Equal"
    value    = "on-demand"
    effect   = "NoSchedule"
  }]
  app_stateful_scheduling = {
    for k, v in {
      opensearch = {
        nodeSelector = local.stateful_node_selector
        tolerations  = local.stateful_tolerations
      }
      "kube-prometheus-stack" = {
        prometheus = { prometheusSpec = {
          nodeSelector = local.stateful_node_selector
          tolerations  = local.stateful_tolerations
        } }
        alertmanager = { alertmanagerSpec = {
          nodeSelector = local.stateful_node_selector
          tolerations  = local.stateful_tolerations
        } }
      }
      # The customer dashboards Grafana (top-level `grafana` key; the kps ops grafana
      # is emptyDir-only and stays on spot). Deep-merged below - base sets env and
      # secret mounts under the same key.
      grafana = {
        nodeSelector = local.stateful_node_selector
        tolerations  = local.stateful_tolerations
      }
    } : k => v if var.cloud == "aws"
  }

  app_values = merge(
    local.app_base_values,
    { for k, v in local.app_azure_values : k => v if k != "gateway" && k != "objectStorage" },
    { gateway = merge(local.app_base_values.gateway, try(local.app_azure_values.gateway, {})) },
    { objectStorage = merge(local.app_base_values.objectStorage, try(local.app_azure_values.objectStorage, {})) },
    { for k, v in local.app_stateful_scheduling : k => v if k != "grafana" },
    { grafana = merge(local.app_base_values.grafana, try(local.app_stateful_scheduling.grafana, {})) },
  )
}

# ---------------------------------------------------------------------------
# Flux bootstrap + the dozuki OCIRepository/HelmRelease that deliver the app.
# ---------------------------------------------------------------------------

resource "kubernetes_namespace_v1" "flux_system" {
  metadata { name = "flux-system" }
}

# The app values, as a Secret the HelmRelease reads via valuesFrom. Managed directly by the
# kubernetes provider (not kubectl apply), so the 240KB size is fine - the create-not-apply
# limit only bit the spike's manual `kubectl apply` (last-applied annotation > 256KB).
resource "kubernetes_secret_v1" "flux_values" {
  metadata {
    name      = "dozuki-flux-values"
    namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
    labels = {
      "app.kubernetes.io/managed-by" = "terraform"
      # helm-controller watches the REFERENCED Secret (not the HelmRelease) for this label and
      # re-reconciles immediately on a values change; without it a values edit waits up to the 30m interval.
      "reconcile.fluxcd.io/watch" = "Enabled"
    }
  }
  data = { "values.yaml" = yamlencode(local.app_values) }
}

# GHCR pull secret in flux-system for the Azure/generic OCIRepository (source-controller pulls the
# chart from ghcr.io there instead of ECR). Mirrors the app-namespace ghcr-pull; AWS/gov use pod
# identity + provider=aws instead, so this is azure-only.
resource "kubernetes_secret_v1" "flux_ghcr_pull" {
  count = var.cloud == "azure" ? 1 : 0
  metadata {
    name      = "flux-ghcr-pull"
    namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
  }
  type = "kubernetes.io/dockerconfigjson"
  data = {
    ".dockerconfigjson" = jsonencode({
      auths = { "ghcr.io" = { auth = base64encode("${var.ghcr_pull_username}:${var.ghcr_pull_token}") } }
    })
  }
}

# Flux controllers (source + helm only). wait=false: `helm install --wait` on the flux chart
# wedges when detached (proven on the spike). Migration-free install, so no token-cap concern.
resource "helm_release" "flux" {
  name       = "flux2"
  namespace  = kubernetes_namespace_v1.flux_system.metadata[0].name
  repository = "oci://ghcr.io/fluxcd-community/charts"
  chart      = "flux2"
  version    = var.flux_chart_version
  wait       = false

  # EKS injects pod-identity creds at pod startup; if source-controller starts before the association
  # (and its policy) exist it comes up credential-less and stays that way until a manual restart. So
  # install Flux only after both are effective. count=0 on non-aws makes these no-op refs there.
  depends_on = [
    aws_eks_pod_identity_association.flux_source_controller,
    aws_iam_role_policy.flux_source_ecr_read,
  ]

  # Only the controllers the app-delivery path needs; the rest add footprint + images to mirror.
  # notification-controller comes up only when a Slack webhook is wired (var.flux_slack_webhook_url);
  # it is what turns the Provider/Alert below into actual Slack posts, so no webhook means no controller.
  values = [yamlencode({
    imageAutomationController = { create = false }
    imageReflectionController = { create = false }
    kustomizeController       = { create = false }
    notificationController    = { create = var.flux_slack_webhook_url != "" }
    # do-not-disrupt on helm-controller ONLY. Karpenter VOLUNTARY DISRUPTION was
    # evicting it mid-upgrade, which kills the reconciliation context and fails the
    # release:
    #
    #   17:25:41  helm upgrade running, "checking resources for changes: 220"
    #   17:28:13  Evicted pod: Underutilized   (helm-controller)
    #             -> Helm upgrade failed ... : context canceled
    #             ~8s later a new pod took leadership
    #   17:28:48  retried, succeeded
    #
    # Flux retries so it self-heals, but the retry is not free: the same eviction
    # during the pre-upgrade migration hook interrupts a running database
    # migration rather than a resource diff, and that is the failed-release state
    # that needs manual recovery. Already observed once as "pre-upgrade hooks
    # failed: context canceled".
    #
    # What this annotation does and does not cover, because the difference matters:
    #   consolidation (the "Underutilized" above) - fully excluded.
    #   drift          - only DELAYED. EKS Auto Mode stamps a 24h
    #                    terminationGracePeriod onto every NodeClaim (verified: it
    #                    is present even where the NodePool spec leaves it unset),
    #                    so drift eventually forces through.
    #   expiry         - same, bounded by that grace against a 336h expireAfter.
    #   spot interruption, node repair, manual deletion - NOT covered at all;
    #                    those are forceful. helm-controller currently lands on the
    #                    spot-preferred pool, so a migration is still not immune to
    #                    losing its node. Migrations need to be resumable; this
    #                    annotation only removes the self-inflicted case.
    #
    # Deliberately not applied to source-controller or notification-controller.
    # Their work is short and idempotent - a re-fetch or a re-send - so eviction
    # there has a low, retryable cost, and every annotated pod is another node
    # consolidation cannot reclaim. helm-controller is the only one holding a long,
    # non-idempotent operation.
    #
    # The value must stay a QUOTED string: Kubernetes annotation values are
    # strings and an unquoted true is rejected at apply time.
    helmController = {
      create      = true
      annotations = { "karpenter.sh/do-not-disrupt" = "true" }
    }
    sourceController = { create = true }
  })]
}

# --- Pod identity so source-controller can pull the OCI chart from ECR ---
# OCIRepository `provider: aws` needs the SA's AWS identity. On EKS Auto Mode pod IMDS is blocked,
# so the spike's token fallback is not durable; production associates the source-controller SA with
# an IAM role that has cross-account ECR read on the chart registry. The chart account's ECR repo
# policy must allow this account (the app-image pulls already establish that cross-account path).
data "aws_iam_policy_document" "flux_source_assume" {
  count = var.cloud == "aws" ? 1 : 0
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["pods.eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "flux_source_controller" {
  count              = var.cloud == "aws" ? 1 : 0
  name               = "${data.aws_eks_cluster.main[0].name}-flux-source-controller"
  assume_role_policy = data.aws_iam_policy_document.flux_source_assume[0].json
}

resource "aws_iam_role_policy" "flux_source_ecr_read" {
  count = var.cloud == "aws" ? 1 : 0
  name  = "ecr-read"
  role  = aws_iam_role.flux_source_controller[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Effect = "Allow", Action = ["ecr:GetAuthorizationToken"], Resource = "*" },
      { Effect = "Allow", Action = [
        "ecr:BatchGetImage",
        "ecr:GetDownloadUrlForLayer",
        "ecr:BatchCheckLayerAvailability",
      ], Resource = "*" }, # scope to the chart repo ARN(s) once the cross-account repo policy is confirmed
    ]
  })
}

resource "aws_eks_pod_identity_association" "flux_source_controller" {
  count           = var.cloud == "aws" ? 1 : 0
  cluster_name    = data.aws_eks_cluster.main[0].name
  namespace       = kubernetes_namespace_v1.flux_system.metadata[0].name
  service_account = "source-controller"
  role_arn        = aws_iam_role.flux_source_controller[0].arn
}

# OCIRepository: the same chart artifact env.hcl already pins (registry + chart_version).
resource "kubectl_manifest" "dozuki_ocirepository" {
  depends_on = [helm_release.flux]
  yaml_body = yamlencode({
    apiVersion = "source.toolkit.fluxcd.io/v1"
    kind       = "OCIRepository"
    metadata   = { name = "dozuki", namespace = kubernetes_namespace_v1.flux_system.metadata[0].name }
    # provider=aws pulls with the source-controller's pod-identity ECR creds (AWS + gov). Azure has no
    # pod identity, so it uses the default provider + the flux-system ghcr dockerconfigjson secret.
    spec = merge(
      {
        interval = "10m"
        url      = "oci://${var.image_repository}/charts/dozuki"
        ref      = { tag = var.chart_version }
        provider = var.cloud == "aws" ? "aws" : "generic"
      },
      var.cloud == "aws" ? {} : { secretRef = { name = "flux-ghcr-pull" } },
    )
  })
}

# HelmRelease: adopts the existing TF-installed release (releaseName/storageNamespace=dozuki), then
# reconciles from the OCIRepository with in-cluster wait + a migration-sized timeout, unbounded by
# any Spacelift token. Values come from the Secret above (built from local.app_values, not a snapshot).
resource "kubectl_manifest" "dozuki_helmrelease" {
  depends_on = [
    helm_release.flux, kubectl_manifest.dozuki_ocirepository, kubernetes_secret_v1.flux_values,
    # same platform-ordering barriers the old helm_release.app depended on:
    helm_release.cert_manager, helm_release.envoy_gateway, helm_release.external_secrets,
    kubernetes_service_account_v1.eso_vault_auth, kubernetes_secret_v1.ghcr_pull,
    aws_eks_addon.cloudwatch_observability, kubernetes_secret_v1.gateway_tls, helm_release.external_dns,
    kubernetes_secret_v1.redis_auth_eg, azurerm_key_vault_secret.app, vault_kv_secret_v2.tls,
    helm_release.istio_base, helm_release.istiod, helm_release.istio_cni, helm_release.ztunnel,
    kubernetes_labels.ambient_dozuki, kubernetes_labels.ambient_envoy_gateway, kubernetes_labels.ambient_redis,
  ]
  yaml_body = yamlencode({
    apiVersion = "helm.toolkit.fluxcd.io/v2"
    kind       = "HelmRelease"
    metadata = {
      name      = "dozuki"
      namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
    }
    spec = {
      interval         = "30m"
      releaseName      = "dozuki"
      targetNamespace  = kubernetes_namespace_v1.app.metadata[0].name
      storageNamespace = kubernetes_namespace_v1.app.metadata[0].name
      maxHistory       = 20 # cap stored release revisions; unbounded history piles up storage-namespace Secrets over time
      chartRef         = { kind = "OCIRepository", name = "dozuki", namespace = kubernetes_namespace_v1.flux_system.metadata[0].name }
      install          = { disableWait = false, timeout = "4h30m", remediation = { retries = 0 } }
      # crds=CreateReplace so chart CRD changes actually apply on upgrade - helm-controller skips CRD updates by default.
      # upgrade retries=2: with retries=0 ONE failed upgrade (an eviction, a
      # not-yet-mirrored image, a transient webhook) stalls the release until a
      # human does the requestedAt+forceAt dance (seen live 2026-07-30). Two
      # retries let it self-heal once the cause clears; remediateLastFailure
      # stays false so a failure never auto-rolls-back over a completed
      # migration. Install keeps retries=0: install remediation uninstalls
      # first, which is not a loop we want running unattended.
      upgrade = { disableWait = false, timeout = "4h30m", crds = "CreateReplace", remediation = { retries = 2, remediateLastFailure = false } }
      # driftDetection=enabled corrects cluster drift back to the chart's desired state. Promoted from warn after a fleet
      # sweep (2026-07-30) found drift on exactly one env, and it was stale old-chart state helm's 3-way merge could never
      # fix: failed upgrade attempts record the new manifest without fully applying it, so the next successful upgrade sees
      # "no delta" and live objects keep pre-upgrade specs forever (one env's BTP rate limits and a missing HTTPRoute
      # timeout). Correction is the only mechanism that heals that class. Hand-patches on live objects now get reverted on
      # the next reconcile - fold wanted changes into values instead. Jobs are ignored (empty-string JSON pointer = the
      # whole object) so a completed migration/db-create Job never registers as drift and never gets recreated/rerun - the
      # reason drift detection was omitted before.
      driftDetection = {
        mode = "enabled"
        ignore = [
          # Completed migration/db-create Jobs must never register as drift.
          { paths = [""], target = { kind = "Job" } },
          # HPAs own spec.replicas on the app tier (app-hpa, nextjs memory-HPA);
          # the chart's static count differs whenever an HPA has scaled, which
          # made every reconcile warn DriftDetected (first seen live on apac).
          { paths = ["/spec/replicas"], target = { kind = "Deployment" } },
        ]
      }
      valuesFrom = [{ kind = "Secret", name = kubernetes_secret_v1.flux_values.metadata[0].name, valuesKey = "values.yaml" }]
    }
  })

  # These guarded the old helm_release.app; they move here with app delivery.
  lifecycle {
    precondition {
      condition     = var.cloud == "aws" || !var.enable_bi
      error_message = "enable_bi is not supported on Azure."
    }
    precondition {
      condition     = var.istio_mesh_state == "disabled" || local.mesh_supported
      error_message = "istio_mesh_state requires commercial AWS EKS (non-GovCloud). Gov needs the phase-2 image mirror; Azure is not supported yet."
    }
  }
}

# ---------------------------------------------------------------------------
# notification-controller -> Slack. Flux posts HelmRelease/OCIRepository events (both failures and
# successes, eventSeverity=info) to a Slack incoming webhook. Entirely gated on var.flux_slack_webhook_url:
# empty (the default) creates NO Secret/Provider/Alert and leaves notification-controller off (above), so
# an env without a webhook wired stays exactly as it was. This layer has no Kustomizations, so the Alert
# scopes to the resources that actually exist here: the app HelmRelease and its OCIRepository source.
# ---------------------------------------------------------------------------

# The webhook URL as a Secret the Provider references by `address` key (never inlined in the Provider spec).
resource "kubernetes_secret_v1" "flux_slack_webhook" {
  count = var.flux_slack_webhook_url != "" ? 1 : 0
  metadata {
    name      = "flux-slack-webhook"
    namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
  }
  data = { address = var.flux_slack_webhook_url }
}

resource "kubectl_manifest" "flux_slack_provider" {
  count      = var.flux_slack_webhook_url != "" ? 1 : 0
  depends_on = [helm_release.flux, kubernetes_secret_v1.flux_slack_webhook]
  yaml_body = yamlencode({
    apiVersion = "notification.toolkit.fluxcd.io/v1beta3"
    kind       = "Provider"
    metadata   = { name = "slack", namespace = kubernetes_namespace_v1.flux_system.metadata[0].name }
    spec = {
      type      = "slack"
      secretRef = { name = kubernetes_secret_v1.flux_slack_webhook[0].metadata[0].name }
    }
  })
}

resource "kubectl_manifest" "flux_slack_alert" {
  count      = var.flux_slack_webhook_url != "" ? 1 : 0
  depends_on = [kubectl_manifest.flux_slack_provider]
  yaml_body = yamlencode({
    apiVersion = "notification.toolkit.fluxcd.io/v1beta3"
    kind       = "Alert"
    metadata   = { name = "dozuki", namespace = kubernetes_namespace_v1.flux_system.metadata[0].name }
    spec = {
      providerRef   = { name = "slack" }
      eventSeverity = "info" # info => both success and failure events; "error" would drop successes
      # Every env posts to the same channel with the identical object title
      # (helmrelease/dozuki.flux-system), so stamp the env identity onto each
      # notification - otherwise fleet messages are indistinguishable.
      # Just env + versions: summary and cluster duplicated env on every fleet
      # stack (eks_cluster_id == customer-environment), tripling the same value.
      eventMetadata = {
        env             = "${var.customer}-${var.environment}"
        chart           = var.chart_version
        app-image       = var.image_tag
        webnextjs-image = var.nextjs_tag
      }
      eventSources = [
        { kind = "HelmRelease", name = "*" },
        { kind = "OCIRepository", name = "*" },
      ]
    }
  })
}

# Hand the running release off to Flux without uninstalling it: forget helm_release.app from state
# but leave the live release installed (Flux then adopts it). No-op on a fresh env that never had it.
removed {
  from = helm_release.app
  lifecycle { destroy = false }
}
