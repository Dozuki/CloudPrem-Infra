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
  # Tags for the frontegg connectivity images in our registry. Two tags because two
  # kinds of artifact: -mirror is upstream byte-for-byte, -mysql-tls is our fork with
  # the migration TLS fix. Both pin the upstream digest they were built from (recorded
  # with the images), so what we shipped is always traceable. Immutable tags, never
  # :latest — the upstream tag moved under us historically.
  frontegg_mirror_tag = "20260726-mirror"
  frontegg_fork_tag   = "20260726-mysql-tls"

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

    webhooks = { enabled = var.enable_webhooks }

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

    memcached = { host = local.memcached_host, enabled = true }

    vault = { enabled = var.cloud == "aws", address = var.vault_address }

    azure = {
      enabled     = var.cloud == "azure"
      tenantId    = var.azure_tenant_id
      keyVaultUri = var.azure_key_vault_uri
      environment = var.azure_environment
    }

    monitoring = { enabled = true }

    # metrics-server ships in the chart (default on) as the single source of truth across
    # onprem+cloud; the EKS addon was retired (#297). args=[] drops the chart's onprem-oriented
    # --kubelet-insecure-tls default (cloud kubelets present proper serving certs), keeping the
    # subchart's secure defaultArgs.
    "metrics-server" = { args = [] }

    dashboards = {
      enabled   = var.enable_dashboards
      jwtSecret = local.dashboards_jwt_secret # (was set_sensitive)
    }

    # The frontegg connectivity images come from OUR registry, not Docker Hub.
    #
    # They are mirrored into the dozukicloud ECR (and follow var.image_repository, so gov
    # and airgapped envs resolve their own registry the same way dozuki-operator does).
    # That removes the private Docker Hub pull entirely: no regcred, no frontegg docker
    # credentials in values, and no dependency on a vendor registry for a product
    # frontegg no longer maintains.
    #
    # event-service and webhook-service are FORKS, not straight mirrors: their migration
    # path ignored TLS. Aurora MySQL 8.4 defaults require_secure_transport ON and refuses
    # the plaintext connection outright, so both crashlooped on migrate and the webhook
    # tier could never start. The forks add dialectOptions.ssl, verified against the RDS
    # CA baked into the image. Hence the different tag: -mysql-tls vs -mirror.
    connectivity = {
      "webhook-service" = {
        image            = { repository = "${var.image_repository}/hybrid-webhook-service" }
        appVersion       = local.frontegg_fork_tag
        imagePullSecrets = []
        messageBroker    = { brokerList = var.msk_bootstrap_brokers }
        # useSSL is a STRING: helm coerces a bare true to a bool and the chart b64encs it.
        mysql         = { host = local.db_master_host, username = local.db_master_username, useSSL = "true" }
        mongo         = { connectionString = "mongodb://dozuki-mongodb/webhooks" }
        configuration = { secrets = { "dozuki-infra-credentials" = { FRONTEGG_WEBHOOK_MYSQL_DB_PASSWORD = "master_password" } } }
      }
      "integrations-service" = {
        image            = { repository = "${var.image_repository}/hybrid-integrations-service" }
        appVersion       = local.frontegg_mirror_tag
        imagePullSecrets = []
        messageBroker    = { brokerList = var.msk_bootstrap_brokers }
        mongo            = { connectionString = "mongodb://dozuki-mongodb/integrations" }
      }
      "event-service" = {
        image            = { repository = "${var.image_repository}/hybrid-event-service" }
        appVersion       = local.frontegg_fork_tag
        imagePullSecrets = []
        database         = { host = local.db_master_host, username = local.db_master_username, useSSL = "true" }
        configuration    = { secrets = { "dozuki-infra-credentials" = { FRONTEGG_EVENTS_MYSQL_DB_PASSWORD = "master_password" } } }
        messageBroker    = { brokerList = var.msk_bootstrap_brokers }
        redis            = { host = "dozuki-redis-master", tls = "false" } # tls stays STRING (was --set-string)
      }
      "connectors-worker" = {
        image            = { repository = "${var.image_repository}/hybrid-connectors-worker" }
        appVersion       = local.frontegg_mirror_tag
        imagePullSecrets = []
        messageBroker    = { brokerList = var.msk_bootstrap_brokers }
        redis            = { host = "dozuki-redis-master", tls = "false" } # tls stays STRING
      }
      "api-gateway" = {
        image            = { repository = "${var.image_repository}/hybrid-api-gateway" }
        appVersion       = local.frontegg_mirror_tag
        imagePullSecrets = []
      }
      # Stops the subchart rendering its regcred Secret. Nothing pulls from Docker Hub
      # now, and the chart's default credentials were literal placeholders that Docker
      # Hub rejected anyway.
      frontegg = { images = { enabled = false } }
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
  app_values = merge(
    local.app_base_values,
    { for k, v in local.app_azure_values : k => v if k != "gateway" && k != "objectStorage" },
    { gateway = merge(local.app_base_values.gateway, try(local.app_azure_values.gateway, {})) },
    { objectStorage = merge(local.app_base_values.objectStorage, try(local.app_azure_values.objectStorage, {})) },
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

  # Only the two controllers the app-delivery path needs; the rest add footprint + images to mirror.
  values = [yamlencode({
    imageAutomationController = { create = false }
    imageReflectionController = { create = false }
    kustomizeController       = { create = false }
    notificationController    = { create = false }
    helmController            = { create = true }
    sourceController          = { create = true }
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
      maxHistory       = 0 # keep unlimited history; helm-controller defaults to 5 and would prune
      chartRef         = { kind = "OCIRepository", name = "dozuki", namespace = kubernetes_namespace_v1.flux_system.metadata[0].name }
      install          = { disableWait = false, timeout = "4h30m", remediation = { retries = 0 } }
      upgrade          = { disableWait = false, timeout = "4h30m", remediation = { retries = 0, remediateLastFailure = false } }
      # driftDetection intentionally omitted: a completed migration Job must not be recreated/rerun.
      valuesFrom = [{ kind = "Secret", name = kubernetes_secret_v1.flux_values.metadata[0].name, valuesKey = "values.yaml" }]
    }
  })

  # These guarded the old helm_release.app; they move here with app delivery.
  lifecycle {
    precondition {
      condition     = var.cloud == "aws" || (!var.enable_webhooks && !var.enable_bi)
      error_message = "enable_webhooks and enable_bi are not supported on Azure."
    }
    precondition {
      condition     = var.istio_mesh_state == "disabled" || local.mesh_supported
      error_message = "istio_mesh_state requires commercial AWS EKS (non-GovCloud). Gov needs the phase-2 image mirror; Azure is not supported yet."
    }
  }
}

# Hand the running release off to Flux without uninstalling it: forget helm_release.app from state
# but leave the live release installed (Flux then adopts it). No-op on a fresh env that never had it.
removed {
  from = helm_release.app
  lifecycle { destroy = false }
}
