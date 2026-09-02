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

  # app_cron_mode -> the deployments/appCron values that implement the crond/CronJobs cutover
  # table (both-dispatching is unrepresentable by construction, see the variable). crond is
  # deliberately the EMPTY diff - no crond or appCron key at all - so a plan on an env that has
  # not opted in renders byte-identical to before this variable existed. shadow and cronjobs
  # each add deployments.crond.replicaCount alongside the existing webNextjs entry, plus the
  # whole appCron block. This is a values table, not a state machine: which mode an env may move
  # to from another is a human/operator-interlock concern (see the variable), not something
  # terraform enforces.
  app_cron_mode_values = {
    crond = {
      deployments = {}
      appCron     = {}
    }
    shadow = {
      deployments = { crond = { replicaCount = 1 } }
      appCron     = { enabled = true, suspend = true }
    }
    cronjobs = {
      deployments = { crond = { replicaCount = 0 } }
      appCron     = { enabled = true, suspend = false }
    }
  }

  # chart_version with any pre-release or build suffix stripped, split into its components:
  # "2.10.27" -> ["2","10","27"], "2.10.24-dashfix.1" -> ["2","10","24"],
  # "2.10.27+build.1" -> ["2","10","27"]. Both separators are cut, not just "-": a "+build"
  # suffix left in place makes the patch component non-numeric and refuses a chart that is
  # actually new enough. Consumers must check length(...) == 3 before indexing; a malformed
  # pin like "4" or "4-anything" splits to a 1-element list whose [0] is numerically 4, which
  # would otherwise satisfy a bare major-only comparison and wave through a version that is
  # not a real chart release.
  chart_version_core = split(".", split("+", split("-", var.chart_version)[0])[0])

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

    # The slim-only keys (flavor, app.path) are only emitted when the flavor is slim, so
    # a legacy env's rendered values stay byte-identical to the pre-flavor shape. The
    # beanstalkd key is different: it is keyed on beanstalkd_tag being set, not on the
    # flavor, because chart 2.10.27 added the dedicated beanstalkd image on the legacy
    # line too. With beanstalkd_tag left empty (the legacy default), this still emits
    # nothing and the values stay byte-identical. That matters because the flux_values
    # Secret below is watched (reconcile.fluxcd.io/watch): any new leaf, even one the
    # chart defaults anyway, changes the Secret and triggers a Helm upgrade on every env
    # at its next infra bump. Conditionals stay split per key: a single conditional
    # mixing a string and an object attribute fails type unification against {}.
    images = merge(
      {
        app = merge(
          { repository = var.image_repository, tag = var.image_tag },
          var.app_image_flavor == "slim" ? { path = "monolith-app" } : {},
        )
        webnextjs = { tag = var.nextjs_tag }
      },
      var.app_image_flavor == "slim" ? { flavor = "slim" } : {},
      var.beanstalkd_tag != "" ? { beanstalkd = { tag = var.beanstalkd_tag } } : {},
    )

    ingress = { hosts = [{ hostname = coalesce(var.ingress_hostname, var.dns_domain_name) }] }

    gateway = merge(
      {
        hosts    = local.app_gateway_hosts
        clientIP = { mode = var.cloud == "azure" ? "none" : "proxyProtocol" }
        stableProxyService = {
          enabled = contains(["aws", "azure"], var.cloud)
          targetGroupBindings = {
            httpsArn = var.cloud == "aws" ? var.nlb_https_target_group_arn : ""
            httpArn  = var.cloud == "aws" ? var.nlb_http_target_group_arn : ""
          }
        }
      },
      # rateLimit is only emitted when set. A `rateLimit = null` value would render into
      # the helm values and blank the chart's defaults instead of leaving them alone.
      { for k, v in { rateLimit = var.gateway_rate_limit } : k => v if v != null },
    )

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
      # Alerts on EBS burst-credit exhaustion, which nothing in-cluster can see:
      # the node stays Ready and StorageReady=True while every disk-backed pod on
      # it stalls. The CloudWatch metric is the only thing that moves.
      #
      # AWS only, and specifically EKS Auto Mode only - the alerts use the EC2
      # instance ID as the node name with no join, which holds because physical
      # turns Auto Mode on unconditionally. Credentials come from the pod-identity
      # association in physical (aws_eks_pod_identity_association.cloudwatch_exporter);
      # without it the pod runs and fails every poll, which is why the chart ships
      # this default-off and it is turned on here rather than there.
      #
      # clusterName scopes discovery to this cluster's nodes via the
      # kubernetes.io/cluster/<name>=owned tag. Leaving it empty is a render
      # error in the chart, on purpose: unscoped it would pull every cluster in
      # the account into this cluster's Prometheus. region is deliberately left
      # to the chart, which falls back to aws.region above.
      #
      # GetMetricData bills per metric requested, not per API call ($0.01 per
      # 1,000), so YACE batching them into one call does not reduce the bill and
      # the 1M-request free tier does not apply. Two metrics per node every 5
      # minutes works out to ~$0.17 per node-month, so a steady 8-node cluster
      # is ~$1.40/month and it scales with the node count.
      cloudwatchExporter = {
        enabled     = var.cloud == "aws" && var.cloudwatch_exporter_enabled
        clusterName = var.eks_cluster_id
        # FIPS endpoints are a compliance choice, not a partition requirement.
        # GovCloud's standard regional endpoints resolve fine; we opt in because
        # gov workloads should stay on validated crypto. YACE only applies this
        # to CloudWatch and falls back to the standard Resource Groups Tagging
        # endpoint either way, so this does not make every call FIPS.
        fips = local.is_us_gov
      }
    }

    # A second copy of every scraped sample, written to the central Mimir on
    # the argo-ops cluster. Additive and reversible: nothing in this cluster's
    # own monitoring depends on it, so setting this false leaves the local
    # Prometheus, its rules and its Alertmanager exactly as they were.
    #
    # Under global, not monitoring, because the push is a field on the Prometheus
    # the kube-prometheus-stack SUBCHART owns. A subchart only ever sees the
    # parent's global block, so monitoring.mimir would render as nothing at all.
    #
    # No key value crosses Terraform. The chart's ExternalSecret reads the
    # per-env ingest token from this env's own Vault path
    # (<customer>/<environment>/mimir, property apiKey), which each env's Vault
    # policy already scopes to itself. That is also why this is AWS only:
    # Azure has no Vault to read it from, and no PrivateLink path to push over.
    #
    # The network half lives in physical under enable_mimir. Turning this on
    # without it leaves Prometheus retrying a name that does not resolve, which
    # the chart's own remote-write alerts catch.
    global = {
      mimir = {
        enabled = var.cloud == "aws" && local.mimir_remote_write_enabled
        url     = local.mimir_url
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
      grafana = {
        url         = var.enable_dashboards ? "http://dozuki-dashboards-grafana" : ""
        primarySite = var.enable_primary_site_grafana
      }
      gatewayAPI = { enabled = var.subsite_gateway_api_enabled }
    }

    grafana = {
      env = {
        # The chart sets serve_from_sub_path but root_url needs the concrete host, which
        # only this layer knows. Without it Grafana's base href stays "/", every asset
        # request lands on the dozuki app instead, and the embedded dashboards render as
        # a blank "failed to load its application files" page. Same host grafana.json's
        # baseUrl uses; /grafana matches the chart's dashboards.subpath default.
        GF_SERVER_ROOT_URL = var.enable_dashboards ? "https://${var.dns_domain_name}/grafana/" : ""
        GF_DATABASE_TYPE   = var.enable_dashboards ? "mysql" : ""
        GF_DATABASE_HOST   = var.enable_dashboards ? "${local.db_master_host}:3306" : ""
        GF_DATABASE_NAME   = var.enable_dashboards ? "grafana_primary" : ""
        GF_DATABASE_USER   = var.enable_dashboards ? local.db_master_username : ""
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
        # dozuki-grafana-db is the release-owned runtime secret the chart renders
        # when grafanaDbInit.enabled (chart >= 2.6.0); it replaced the TF-managed
        # grafana-db-credentials secret + grafana-db-create job from bi.tf. The
        # chart's contract: whoever points Grafana here must set
        # grafanaDbInit.enabled from the same condition, which the block below
        # does (enable_dashboards implies it).
        GF_DATABASE_PASSWORD = { secretKeyRef = {
          name = var.enable_dashboards ? "dozuki-grafana-db" : ""
          key  = var.enable_dashboards ? "password" : ""
        } }
      }
    }

    # Creates the grafana_primary database before anything starts (pre-install/
    # pre-upgrade hook in the chart). enabled covers the BI-only case; the chart
    # also renders the hook whenever dashboards.enabled.
    grafanaDbInit = {
      enabled       = var.enable_bi || var.enable_dashboards
      host          = local.db_master_host
      adminUsername = local.db_master_username
      adminPassword = local.db_master_password
      # The chart's default repository is the commercial dozukicloud ECR, which gov
      # cannot reach (found live on sharedgov 2026-08-03: the first hook run sat in
      # ImagePullBackOff and wedged the 2.7.1 upgrade in a fail/rollback loop). Derive
      # the host from the env's own registry instead: identical to the default on
      # commercial, the haul-mirrored copy on gov. Tag stays the chart default.
      image = {
        repository = "${split("/", var.image_repository)[0]}/mysql-client"
      }
    }

    googleTranslate = { token = var.google_translate_api_token } # (was set_sensitive)

    scheduling = {
      preferSpot = {
        app = var.prefer_spot_app
      }
    }

    deployments = { webNextjs = { env = local.app_webnextjs_env } }
  }

  # Final values = base, merged with the azure-only block. Three keys collide between the two and all
  # three need a one-level-deeper merge (a shallow spread would replace base's whole subtree):
  #   - gateway: base sets hosts/clientIP/stableProxyService; azure adds service/dnsTarget.
  #   - objectStorage: base sets kmsKey/buckets/endpoint/credentials (the old set list); azure's
  #     seaweedfs_values adds publicBackend. Without the deep merge azure would keep only publicBackend
  #     and drop the buckets/credentials the app needs.
  #   - global: base sets mimir; azure adds imagePullSecrets/seaweedfs. Shallow, azure would drop
  #     mimir - harmless today (mimir is aws-only) but it would silently break the moment anything
  #     else lands under global on both sides.
  # Every other azure key (cert_manager, seaweedfs...) has no base collision and shallow-merges
  # cleanly. helm_release.app got the same effect from helm's deep merge of its two values files + set
  # list. All three deep-merges are unconditional (no cond?{}:{} ternary): on non-azure app_azure_values
  # has none of the keys, so try(...,{}) yields {} and the merged result equals base.
  # AWS-only: pin every EBS-backed subchart workload to the on-demand NodePool.
  #
  # These pods (opensearch, prometheus, alertmanager) each own a zonal EBS volume -
  # exactly three, verified by PVC live on m3-apac and dev-min. On the spot pool, an
  # AZ-wide spot shortage left them Pending with no legal fallback - the volume pins
  # the zone, the on-demand pool's taint blocked entry, and that is the old spot-fleet
  # stranding incident reproduced (adversarial AZ/PVC review, 2026-07-28; observed
  # live: all three volumes on one spot c4.xlarge).
  # This list used to name dashboards-grafana too. That was wrong: it is MySQL-backed
  # and owns no PVC (pvc=NONE live in every env checked). See the de-pin note below.
  # On-demand placement plus the pod-level do-not-disrupt annotation set below
  # (local.do_not_disrupt_annotation) makes their disruptions rare and their
  # replacement capacity reliably provisionable in the volume's AZ. The pool
  # itself no longer carries a pool-wide WhenEmpty policy for this - see the
  # disruption block on kubernetes.tf's on-demand NodePool.
  #
  # Lives HERE and not in chart defaults on purpose: the selector/toleration reference
  # Karpenter/EKS labels that do not exist on onprem or AKS clusters - baked into the
  # chart they would make these pods unschedulable there. Same reason the chart's
  # dozuki.onDemand* helpers gate on aws.enabled. The `for..if` idiom (not cond?{}:{})
  # matches app_azure_values - see the type-unification note above it.
  #
  # Select the pool by NAME, not by capacity-type. capacity-type is an attribute the spot
  # pool also sets: it requests capacity-type In [spot, on-demand] as an ICE fallback, so a
  # spot-pool node running on-demand capacity carries capacity-type=on-demand and satisfies
  # this selector. With the spot pool outweighing this one 100 to 10 it won every time, the
  # on-demand pool never launched a node at all, and its NoSchedule taint therefore never
  # kept anything off these nodes - opensearch shared a node with untainted workloads until
  # the node ran out of page cache. Naming the pool is what actually engages both the taint
  # and the pool's disruption policy (2026-08-01).
  stateful_node_selector = { "karpenter.sh/nodepool" = "on-demand" }
  stateful_tolerations = [{
    key      = "eks.amazonaws.com/capacity-type"
    operator = "Equal"
    value    = "on-demand"
    effect   = "NoSchedule"
  }]
  # karpenter.sh/do-not-disrupt="true" on the EBS-volume owners below - not on every pod
  # here, see the DE-PIN note further down and opensearch's replica-count conditional. The
  # value must stay a QUOTED string: Kubernetes annotation values are strings and an
  # unquoted true is rejected at apply time. This is what lets kubernetes.tf's on-demand NodePool run
  # WhenEmptyOrUnderutilized: the annotation exempts these specific pods' nodes
  # from voluntary consolidation instead of exempting the whole pool the way
  # WhenEmpty used to. Verified against the vendored subchart values (helm/chart
  # Chart.yaml pins): opensearch 3.4.0 has a top-level `podAnnotations`, kube-
  # prometheus-stack 82.8.0's Prometheus/Alertmanager CRs take
  # `{prometheus,alertmanager}Spec.podMetadata.annotations`, grafana 11.2.3 and
  # metrics-server 3.13.1 and prometheus-adapter 5.3.0 all have a top-level
  # `podAnnotations` consumed by their Deployment templates.
  #
  # DE-PIN 2026-08-12: the annotation is now carried by the three EBS-volume owners
  # ONLY. dashboards-grafana, metrics-server and prometheus-adapter keep their
  # on-demand nodeSelector and tolerations but LOST the annotation. A pin audit found
  # that none of the three owns a volume, so the stranding argument above never
  # applied to them; each was instead pinning whatever node it happened to land on.
  # Observed cost was ~5 pinned on-demand nodes per env against the ~2 this comment
  # predicts, because the volume trio already co-locates. Thinning the annotated set
  # is what the pool's WhenEmptyOrUnderutilized policy was designed around, not a
  # departure from it.
  #
  # Accepted consequence: consolidation may now move these three, and metrics-server
  # and prometheus-adapter are the HPA control plane, so a scale-up can briefly
  # compute on stale metrics while they reschedule. Short gaps are the approved
  # posture. A stall still live 5 minutes after the pod is Ready again is NOT - treat
  # that as a regression, not as the mechanical cost. dashboards-grafana stays
  # single-replica with no PDB by decision: at one replica a minAvailable=1 PDB makes
  # the pod undrainable, Karpenter respects it, and the pin comes back under another
  # name with the savings forfeited. Do not add one here.
  #
  # Rollout note: this changes every one of these pod templates, so each env
  # rolls opensearch, prometheus, alertmanager and the customer grafana once -
  # a real EBS detach/reattach and a search/metrics gap, the same event the
  # annotation exists to stop happening again - the first time that env takes
  # the infra_version bump carrying this change. Not a background no-op;
  # sequence it per env like any other stateful rollout (dev-min/qa first).
  #
  # Floor: a pod carrying this annotation pins its node against consolidation for as
  # long as the pod lives there. How many nodes that costs depends on opensearch's
  # replica count, since the annotation below is conditional on it - 1 node in the
  # collapsed prod envs where opensearch runs 3 replicas and is unpinned, leaving
  # prometheus and alertmanager to share one, and 2-3 elsewhere. The measurement and
  # the full breakdown live on the on-demand NodePool's disruption block in
  # kubernetes.tf; keep the two in step.
  do_not_disrupt_annotation = { "karpenter.sh/do-not-disrupt" = "true" }
  app_stateful_scheduling = {
    for k, v in {
      opensearch = {
        nodeSelector = local.stateful_node_selector
        tolerations  = local.stateful_tolerations
        # Pin stays below 3 replicas (a singleton or a transitional 2-node cluster
        # still wants the 24h drift delay); at 3 the subchart PDB governs drains and
        # the pin would only turn graceful drains back into force-kills, which is
        # what took search down on one env 2026-08-12.
        podAnnotations = var.opensearch_replicas >= 3 ? {} : local.do_not_disrupt_annotation
        # singleNode=false drops discovery.type=single-node, and THAT is what makes the
        # bootstrap checks fatal: with zen discovery and a non-loopback network.host
        # (the chart sets 0.0.0.0) OpenSearch enters production mode and refuses to
        # start if a limit is short, instead of just warning. The one that actually
        # varies by node OS is vm.max_map_count, which nothing in this repo or the chart
        # sets - the subchart's sysctl/sysctlInit are both false and stay false.
        # Verified 2026-08-13 on all nine AWS MPC envs: every node is Bottlerocket (EKS
        # Auto) and reports vm.max_map_count 1048576, 4x the required 262144, with
        # nofile 65536 and nproc/fsize unlimited. So the checks pass on the fleet as it
        # stands. If a future env ever runs a node OS that ships the AL2023-style 65530,
        # its opensearch pod will CrashLoopBackOff the moment it takes this flip - set
        # sysctlInit.enabled there (a pod-level `sysctls` entry will not work, kubelet
        # rejects vm.max_map_count as unsafe). Re-check this when the node OS changes.
        singleNode = false
        replicas   = var.opensearch_replicas
        # NO readinessProbe override - the subchart's port-up default is kept on
        # purpose. Ready therefore does not mean joined, and the migration tolerates
        # that: split-brain safety is bootstrap arithmetic (a fresh pod can never be
        # a majority of cluster.initial_master_nodes), not readiness, so a premature
        # roll can only cost a bounded availability wobble. A cluster-health
        # readiness probe was evaluated and rejected: it couples every pod's Ready to
        # master state (endpoint removal amplifies a masterless transit into a full
        # outage) and can wedge a rolling update behind a probe that cannot succeed.
        #
        # The subchart applies topologySpreadConstraints with a bare toYaml (no tpl),
        # so these labels must be literals. minDomains (default 3) forces the scheduler
        # to evaluate across all required AZ domains even when an AZ currently carries
        # no matching on-demand node, triggering Karpenter to provision capacity rather
        # than colocating replicas and risking quorum loss (Lodestar-cmn). Set minDomains
        # per-env (e.g. 2 on emea) for regions with fewer than 3 usable AZs.
        topologySpreadConstraints = [{
          maxSkew           = 1
          minDomains        = var.opensearch_min_domains
          topologyKey       = "topology.kubernetes.io/zone"
          whenUnsatisfiable = "DoNotSchedule"
          labelSelector = { matchLabels = {
            "app.kubernetes.io/instance" = "dozuki"
            "app.kubernetes.io/name"     = "opensearch"
          } }
        }]
      }
      "kube-prometheus-stack" = {
        prometheus = { prometheusSpec = {
          nodeSelector = local.stateful_node_selector
          tolerations  = local.stateful_tolerations
          podMetadata  = { annotations = local.do_not_disrupt_annotation }
        } }
      }
      # The customer dashboards Grafana (top-level `grafana` key; the kps ops grafana
      # is emptyDir-only and stays on spot). Deep-merged below - base sets env and
      # secret mounts under the same key.
      # De-pinned 2026-08-12 (see the DE-PIN note above): MySQL-backed, owns no PVC.
      # Stays on the on-demand pool, but consolidation may now repack it.
      grafana = {
        nodeSelector = local.stateful_node_selector
        tolerations  = local.stateful_tolerations
      }
      # Not EBS-backed, but the HPA control plane: metrics-server serves metrics.k8s.io
      # and prometheus-adapter serves external.metrics.k8s.io, and both are single
      # replica. The incident that first pinned them was on the SPOT pool, where
      # consolidation churn left every HPA computing on stale/absent metrics (a
      # commercial env: FailedGetExternalMetric x45 over 5d, 30-min metric gaps).
      # Moving them to the on-demand pool is what actually fixed that; the annotation
      # was belt-and-braces on top and cost a pinned node each. De-pinned 2026-08-12 -
      # the nodeSelector below is the protection that matters. A PDB is still the wrong
      # tool at 1 replica (it only delays eviction until the force-delete), so do not
      # add one. Deep-merged below - base sets args under metrics-server.
      "metrics-server" = {
        nodeSelector = local.stateful_node_selector
        tolerations  = local.stateful_tolerations
      }
      "prometheus-adapter" = {
        nodeSelector = local.stateful_node_selector
        tolerations  = local.stateful_tolerations
        # Was running with zero requests fleet-wide (5+ envs confirmed live), which
        # put it in BestEffort QoS and made it invisible to Karpenter's bin-packing
        # math - it occupied a node slot for free and was first in line for
        # eviction under node pressure, the opposite of what the on-demand pin was
        # for. Values are modest and honest: measured actual usage across the
        # fleet is well under this, so the request just makes the pod visible to
        # bin-packing rather than reserving real headroom.
        resources = {
          requests = {
            cpu    = "25m"
            memory = "64Mi"
          }
        }
      }
    } : k => v if var.cloud == "aws"
  }

  app_values = merge(
    local.app_base_values,
    { for k, v in local.app_azure_values : k => v if k != "gateway" && k != "objectStorage" && k != "global" },
    { gateway = merge(local.app_base_values.gateway, try(local.app_azure_values.gateway, {})) },
    { objectStorage = merge(local.app_base_values.objectStorage, try(local.app_azure_values.objectStorage, {})) },
    { global = merge(local.app_base_values.global, try(local.app_azure_values.global, {})) },
    { for k, v in local.app_stateful_scheduling : k => v if k != "grafana" && k != "metrics-server" },
    { grafana = merge(local.app_base_values.grafana, try(local.app_stateful_scheduling.grafana, {})) },
    { "metrics-server" = merge(local.app_base_values["metrics-server"], try(local.app_stateful_scheduling["metrics-server"], {})) },
    # app_cron_mode: deployments deep-merges with the webNextjs entry already set above; appCron is
    # a new top-level key, omitted entirely (length 0) in the default crond mode so the rendered
    # values are byte-identical to before this variable existed.
    { deployments = merge(local.app_base_values.deployments, local.app_cron_mode_values[var.app_cron_mode].deployments) },
    { for k, v in local.app_cron_mode_values[var.app_cron_mode] : k => v if k != "deployments" && length(v) > 0 },
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

  # slim needs the chart's images.flavor contract, first shipped in chart 2.11.0. A
  # pre-flavor chart silently ignores images.flavor and renders the legacy beanstalkd
  # and crond entrypoints against the slim image (which carries neither), so the plan
  # passes and the workloads can't start. Fail that combination at plan time instead.
  lifecycle {
    precondition {
      condition = var.app_image_flavor != "slim" || try(
        tonumber(split(".", var.chart_version)[0]) > 2 ||
        (tonumber(split(".", var.chart_version)[0]) == 2 && tonumber(split(".", var.chart_version)[1]) >= 11),
        false
      )
      error_message = "app_image_flavor=slim requires chart_version >= 2.11.0 (first release with images.flavor support)."
    }

    # beanstalkd_tag on the legacy flavor needs the chart that taught images.beanstalkd.tag
    # to the legacy line: 2.10.27 on the 2.x/release-2.x branch, or (once the 3.x/main port
    # lands) 4.0.0+ on that line. 4.0.0 is a placeholder ceiling, not a real floor yet - lower
    # it once main ships the equivalent. Before either floor, an older chart silently ignores
    # the value and leaves beanstalkd running on the app image, so make that failure loud here
    # instead of letting it surface as a workload that never gets the dedicated image.
    #
    # chart_version can carry a pre-release/build suffix (gov hotfixes are cut this way, e.g.
    # dozuki-gov is pinned to "2.10.24-dashfix.1" today), and split(".", ...) on that string
    # leaves a component like "27-hotfix" that tonumber() can't parse, tripping the try(...,
    # false) fallback and failing the precondition even when the chart is new enough. So the
    # comparison runs against local.chart_version_core, which strips the suffix first. This
    # treats "2.10.27-hotfix.1" as satisfying >= 2.10.27, which is correct here: a hotfix tag
    # cut off 2.10.27 does contain the chart change. This is not full semver pre-release
    # ordering, just enough to stop a real chart version from reading as too old.
    #
    # length(...) == 3 is load-bearing, not defensive noise: without it a malformed pin of
    # "4" or "4-anything" parses to a single component that is numerically 4, satisfies the
    # major-only arm, and SILENTLY authorises the tag on a chart that does not support it -
    # the exact silent-pass this precondition exists to prevent. Any parse failure or short
    # list falls through try(..., false) to a loud refusal, which is the safe direction.
    precondition {
      condition = (
        var.app_image_flavor == "slim" ||
        var.beanstalkd_tag == "" ||
        try(
          length(local.chart_version_core) == 3 && (
            tonumber(local.chart_version_core[0]) >= 4 ||
            (tonumber(local.chart_version_core[0]) == 2 && (
              tonumber(local.chart_version_core[1]) > 10 ||
              (tonumber(local.chart_version_core[1]) == 10 && tonumber(local.chart_version_core[2]) >= 27)
            ))
          ),
          false
        )
      )
      error_message = "beanstalkd_tag is set on the legacy flavor but chart_version is below the floor that supports it (>= 2.10.27, or >= 4.0.0 once the main line ships this). An older chart silently ignores the value and leaves beanstalkd running on the app image instead of the dedicated one."
    }
  }
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
  # notification-controller comes up only when Slack delivery is wired (bot token or webhook);
  # it is what turns the Provider/Alert below into actual Slack posts, so no transport means no controller.
  values = [yamlencode({
    imageAutomationController = { create = false }
    imageReflectionController = { create = false }
    kustomizeController       = { create = false }
    notificationController = {
      create    = local.flux_slack_enabled
      resources = { requests = { memory = "256Mi" } }
    }
    # helm-controller is deliberately NOT do-not-disrupt pinned. The pin used to live
    # right here; it now sits on the db-migrations Job pod template instead (dozuki chart,
    # dbMigrations.pinAgainstDisruption, default true, chart 2.10.24 and later).
    #
    # NOTHING ENFORCES THAT 2.10.24 FLOOR. var.chart_version is per env, so pinning an env
    # below 2.10.24 would drop BOTH pins at once and leave migrations unprotected. There is
    # a lifecycle precondition pattern in this file (see app_image_flavor below) but it is
    # not used here on purpose: chart_version legitimately carries 3.x on dev-slim and
    # pre-release strings like 2.10.24-dashfix.1 on gov, so a naive numeric compare would
    # reject valid pins. Every fleet env is past 2.10.24 today; treat a downgrade below it
    # as the thing to catch in review.
    #
    # The evidence that put a pin here in the first place, kept because it is still the
    # reason the migration needs protecting - Karpenter VOLUNTARY DISRUPTION evicting
    # helm-controller mid-upgrade kills the reconciliation context and fails the release:
    #
    #   17:25:41  helm upgrade running, "checking resources for changes: 220"
    #   17:28:13  Evicted pod: Underutilized   (helm-controller)
    #             -> Helm upgrade failed ... : context canceled
    #             ~8s later a new pod took leadership
    #   17:28:48  retried, succeeded
    #
    # Flux retries, so that case self-heals. The expensive case was the same eviction
    # landing while the database migration was running rather than during a resource diff
    # (observed then as "pre-upgrade hooks failed: context canceled", back when the
    # migration was a Helm pre-upgrade hook).
    #
    # It is no longer a hook, and that matters here. The dozuki chart renders db-migrations
    # as a plain Job whose NAME is a content hash of the app image tag, db.resourceId and
    # the pod template (dozuki.migrationJobName). There is no helm.sh/hook annotation on it,
    # so a retried upgrade re-applies the identical Job name instead of re-running a hook:
    # no second concurrent migration against non-transactional MySQL DDL, and a completed
    # Job is simply reused. Checked against helm origin/release-2.x b169fdd. This is the
    # failure mode that made the Job pin the right home for the annotation, and it is the
    # first thing to re-verify if the chart ever turns db-migrations back into a hook.
    #
    # Why the Job pin is the better instrument. The migration is the operation whose
    # interruption costs the most and is least recoverable, and the Job pin's scope matches
    # it: Karpenter ignores terminal-phase pods, so the annotation affects consolidation
    # only while the migration pod is still running. Pinning helm-controller instead held a
    # whole node out of consolidation permanently to protect a window measured in minutes -
    # measured 2026-08-21 (Lodestar-02z.21), helm-controller accounted for one of the two
    # pinned nodes in every collapsed env, and it has no nodeSelector or affinity so it
    # lands somewhere different in each one.
    #
    # Residual risk, accepted and NOT fully covered - state it honestly. helm-controller
    # interrupted mid-helm-upgrade can leave the release pending-upgrade.
    #   - terminationGracePeriodSeconds is 600 (confirmed by rendering
    #     fluxcd-community/flux2 at the pinned var.flux_chart_version; source-controller
    #     renders 10). That is an OPPORTUNITY to finish, not a guarantee: the incident
    #     above shows the controller cancelling its context and a new pod taking leadership
    #     ~8s after eviction began, nowhere near the grace window.
    #   - upgrade.strategy=RetryOnFailure on the HelmRelease below is the actual recovery
    #     mechanism - see the long note there. It covers a failed reconciliation. Whether
    #     it always clears a Helm release left locked in pending-upgrade has not been
    #     verified on 1.6.2, so manual Helm state recovery stays on the table.
    #   - Forceful disruption (spot interruption, node repair, manual delete, host failure)
    #     was never covered by the annotation either, and helm-controller has no placement
    #     constraint keeping it off those paths.
    #
    # Still deliberately not applied to source-controller or notification-controller either.
    # Their work is short and idempotent - a re-fetch or a re-send - so eviction there has a
    # low, retryable cost.
    #
    # Memory requests below are measured, not the chart defaults. All three controllers
    # ship a 64Mi request and every one of them runs several times that in every env
    # (14d window, 9 envs: helm-controller max 214Mi, source and notification 198Mi
    # each). A request that far under real usage makes the pod look nearly free to
    # bin-packing and ranks it worse under node memory pressure, where kubelet sorts by
    # usage relative to request. That route was never covered by the annotation anyway, so
    # a truthful request is what keeps helm-controller's memory-pressure eviction risk low;
    # the Karpenter annotation never had any bearing on this path (kubelet does not read
    # it). Values are F2 (max working set x 1.2, rounded up to 32Mi). No limits: an
    # OOMKilled helm-controller mid-upgrade is the failure mode being designed out.
    helmController = {
      create    = true
      resources = { requests = { memory = "288Mi" } }
    }
    sourceController = {
      create    = true
      resources = { requests = { memory = "256Mi" } }
    }
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
      #
      # upgrade.strategy=RetryOnFailure (needs helm-controller >= v1.4.0; the fleet runs v1.6.2
      # from the flux2 chart pin above) REPLACES the remediation stanza that used to sit here.
      # The schema still accepts both together, but at runtime strategy wins outright: on a
      # failed release the controller checks for an active retry strategy and returns a plain
      # upgrade before it ever reads the remediation config. So upgrade.remediation's retries,
      # remediateLastFailure and its own inner `strategy` (rollback/uninstall, not this one)
      # would all be dead settings, and the block is deleted rather than left to mislead.
      #
      # Why retry rather than remediate. Remediation's default strategy is rollback, and a
      # rollback here downgrades the chart and re-runs the OLD chart's pre-upgrade db-migrations
      # migration against MySQL DDL that is not transactional. Nothing guards that: the
      # db-migrations Job's own do-not-disrupt pin covers node disruption, not Helm
      # re-executing an older chart's migration. Choosing retry over remediate is the
      # guard. Rollback also has a dead end: when
      # .status.history carries no prior snapshot - a HelmRelease that ADOPTED a pre-existing helm
      # release and has not yet completed a Flux-driven upgrade - there is nothing to roll back
      # to, so the release goes
      # Stalled=True / MissingRollbackTarget and sits there until a human forces a reconcile.
      # dev-min hit exactly that on 2026-08-12 when a consolidation storm evicted
      # cert-manager-webhook mid-upgrade. RetryOnFailure cannot reach that state at all: the
      # missing-rollback-target error is only raised on the rollback branch, which the retry path
      # never enters.
      #
      # What it costs, written down because it is not obvious. Retries are UNBOUNDED and the
      # release never reaches a terminal Stalled state (fluxcd/helm-controller#1551 is open to add
      # a bound - take it when it ships). Between failed attempts, the release sits at Ready=False
      # (reason=UpgradeFailed) for the duration of the retryInterval (15m), which the app chart's
      # FluxHelmReleaseNotReady alert (gotk_resource_info ready="False", for 15m) catches as the
      # primary detector. The sibling FluxHelmReleaseStuck alert (ready="Unknown", for 30m) catches
      # in-flight reconciliations that hang without completing or failing. The Slack Alert further
      # down also forwards each attempt's UpgradeFailed event. When you see one retrying, fix the
      # cause and leave it alone - the next interval picks the fix up on its own, no forced
      # reconcile needed.
      #
      # Third consequence, easy to miss: every failed attempt writes a helm revision, so at a
      # 15m interval a broken release churns through maxHistory (20, above) within hours and
      # the stored history ends up all-failed. Nothing in this configuration depends on that
      # history, since this strategy never rolls back, but a human reaching for `helm rollback`
      # or a future revert to remediation would find a poor target. Fix forward.
      #
      # retryInterval 15m rather than the 5m default: upgrades are merge-triggered and not
      # latency-critical, and every attempt re-runs the pre-upgrade db-migrations Job, so a
      # genuinely broken release churns 4 hook Jobs an hour instead of 12.
      #
      # Install is deliberately left alone: remediation.retries=0, no strategy block. Be clear
      # about what that costs, because it is the one remaining path that stalls for good. The
      # install path is reachable whenever helm storage holds no release - a fresh env, or the
      # uninstall-and-rerun recovery runbook - and Karpenter is live for the whole 4h30m install
      # window, so a mid-install eviction leaves a failed release that a human has to reconcile.
      # Accepted: an install is human-initiated either way, so the failure lands on someone who
      # is already watching, and an unboundedly retrying install has no prior good release
      # underneath it to fall back on.
      # The retry strategy above does NOT cover this transitively. The controller picks the
      # install-vs-upgrade config from status.lastAttemptedReleaseAction, so a first-install
      # failure resolves to the install settings on every later reconcile and stays failed
      # (helm-controller v1.6.2, actionForState). Only once a release has succeeded at least
      # once does a later failure resolve to the upgrade settings and pick up RetryOnFailure.
      #
      # upgrade.force pinned false for the same reason as rollback.force below. It is the knob
      # people reach for to punch through immutable-field errors, where it makes things worse.
      # upgrade.timeout is per-attempt and env-tunable (var.flux_upgrade_timeout, default 4h30m).
      # Its floor is db_migrations_active_deadline_seconds plus headroom, not an arbitrary number:
      # helm blocks on the db-migrations Job, so a timeout below the Job's own deadline cuts a
      # migration off part way.
      upgrade = {
        disableWait = false
        timeout     = var.flux_upgrade_timeout
        crds        = "CreateReplace"
        force       = false
        strategy    = { name = "RetryOnFailure", retryInterval = "15m" }
      }
      # Both force knobs pinned false explicitly. That is already the Helm default, but the
      # semantics are widely misread, so they get written down rather than inherited.
      # What force actually does, checked against source (helm v3.19.0 pkg/kube/client.go
      # updateResource, v4.0.0 replaceResource): it swaps the patch for helper.Replace, a
      # whole-object PUT. It does NOT delete and recreate, so it will not re-run a completed
      # db-migrations Job. The reason to keep it off is the opposite of the usual pitch. A
      # PUT sends only what the chart renders, so any field the server populated and the
      # chart does not carry is dropped or makes the PUT fail outright, and an immutable
      # field whose live value was server-defaulted (exactly the auto-stamped Job selector
      # this chart change removes) turns a survivable patch into a hard failure. It is an
      # all-or-nothing write with no upside on this release.
      # Scope: helm v4 refuses force-replace together with server-side apply, and the
      # HelmRelease CRD documents force as ignored under SSA. The flux_chart_version pinned
      # here is on the SSA line, so today both pins are defense-in-depth for client-side or
      # adopted releases rather than an active guard. Neither governs the ordinary
      # create/prune cycle that happens when the Job name changes.
      # spec.rollback only governs a remediation rollback, and under RetryOnFailure none ever
      # runs, so this setting is dormant. It stays pinned so the semantics are already correct if
      # the strategy is ever reverted.
      rollback = { force = false }
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
          # made every reconcile warn DriftDetected (first seen live on a commercial env).
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
  }
}

# ---------------------------------------------------------------------------
# notification-controller -> signed fleet relay -> Slack. Flux posts HelmRelease/OCIRepository events
# (both failures and successes, eventSeverity=info) as generic-hmac payloads. The relay verifies the
# partition-local Vault token, renders the approved card format, and correlates one desired revision's
# source + release events into a deployment thread. Direct bot-token/webhook delivery remains only as
# a migration fallback; the relay wins when enabled. With no transport, notification-controller stays
# off. This layer has no Kustomizations, so the Alert scopes to the app HelmRelease and OCIRepository.
# ---------------------------------------------------------------------------

locals {
  flux_slack_use_relay = var.cloud == "aws" && var.flux_slack_relay_enabled
  # Legacy direct transports stay available for old partitions, but never win
  # over the relay's controllable Block Kit renderer.
  flux_slack_use_token = !local.flux_slack_use_relay && var.flux_slack_bot_token != "" && var.flux_slack_channel != ""
  flux_slack_enabled   = local.flux_slack_use_relay || local.flux_slack_use_token || var.flux_slack_webhook_url != ""
}

check "flux_slack_relay_aws_only" {
  assert {
    condition     = !var.flux_slack_relay_enabled || var.cloud == "aws"
    error_message = "flux_slack_relay_enabled requires an AWS-backed environment with the partition-local Vault relay."
  }
}

# Legacy direct credential. Relay-enabled environments replace this with the
# ExternalSecret below, so the Slack bot token no longer enters the cluster.
resource "kubernetes_secret_v1" "flux_slack_webhook" {
  count = local.flux_slack_enabled && !local.flux_slack_use_relay ? 1 : 0
  metadata {
    name      = "flux-slack-webhook"
    namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
  }
  data = local.flux_slack_use_token ? { token = var.flux_slack_bot_token } : { address = var.flux_slack_webhook_url }
}

# The app chart's SecretStore is namespace-scoped to dozuki. Give flux-system
# its own identity and narrowly scoped Vault role. This namespace can read only
# the endpoint + relay HMAC token; the Slack bot token remains server-side in
# the relay's SSM cache.
resource "kubernetes_service_account_v1" "flux_external_secrets" {
  count = local.flux_slack_use_relay ? 1 : 0

  metadata {
    name      = "dozuki-external-secrets"
    namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
  }
}

resource "kubectl_manifest" "flux_relay_secret_store" {
  count = local.flux_slack_use_relay ? 1 : 0
  depends_on = [
    helm_release.external_secrets,
    kubernetes_service_account_v1.flux_external_secrets,
    vault_kubernetes_auth_backend_role.flux_relay,
  ]
  yaml_body = yamlencode({
    apiVersion = "external-secrets.io/v1"
    kind       = "SecretStore"
    metadata = {
      name      = "flux-relay-vault"
      namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
    }
    spec = {
      provider = {
        vault = {
          server  = var.vault_address
          path    = "secret"
          version = "v2"
          auth = {
            kubernetes = {
              mountPath = "k8s/${local.vault_stack_label}"
              role      = "flux-slack-relay"
              serviceAccountRef = {
                name      = kubernetes_service_account_v1.flux_external_secrets[0].metadata[0].name
                audiences = [var.vault_address]
              }
            }
          }
        }
      }
    }
  })
}

# This serves Flux's own notification path (HelmRelease and Kustomization
# failures), not Alertmanager. The "alertmanager-slack-relay" prefix on the
# Vault paths below is historical: it names the shared relay service, not an
# Alertmanager consumer. Do not delete these as Alertmanager remnants.
resource "kubectl_manifest" "flux_relay_external_secret" {
  count      = local.flux_slack_use_relay ? 1 : 0
  depends_on = [kubectl_manifest.flux_relay_secret_store]
  yaml_body = yamlencode({
    apiVersion = "external-secrets.io/v1"
    kind       = "ExternalSecret"
    metadata = {
      name      = "flux-slack-relay"
      namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
    }
    spec = {
      refreshInterval = "5m"
      secretStoreRef  = { name = "flux-relay-vault", kind = "SecretStore" }
      target = {
        name           = "flux-slack-relay"
        creationPolicy = "Owner"
        deletionPolicy = "Retain"
        template = {
          engineVersion = "v2"
          data = {
            address = "{{ .url }}"
            token   = "{{ .token }}"
          }
        }
      }
      data = [
        {
          secretKey = "url"
          remoteRef = {
            key      = "dozuki/global/alertmanager-slack-relay-endpoint"
            property = "url"
          }
        },
        {
          secretKey = "token"
          remoteRef = {
            key      = "dozuki/global/alertmanager-slack-relay"
            property = "token"
          }
        },
      ]
    }
  })
}

resource "kubectl_manifest" "flux_slack_provider" {
  count = local.flux_slack_enabled ? 1 : 0
  depends_on = [
    helm_release.flux,
    kubernetes_secret_v1.flux_slack_webhook,
    kubectl_manifest.flux_relay_external_secret,
  ]
  yaml_body = yamlencode({
    apiVersion = "notification.toolkit.fluxcd.io/v1beta3"
    kind       = "Provider"
    metadata   = { name = "slack", namespace = kubernetes_namespace_v1.flux_system.metadata[0].name }
    spec = merge(
      {
        type = local.flux_slack_use_relay ? "generic-hmac" : "slack"
        secretRef = {
          name = local.flux_slack_use_relay ? "flux-slack-relay" : kubernetes_secret_v1.flux_slack_webhook[0].metadata[0].name
        }
      },
      !local.flux_slack_use_relay && local.flux_slack_use_token ? {
        address = "https://slack.com/api/chat.postMessage"
        channel = var.flux_slack_channel
      } : {}
    )
  })
}

resource "kubectl_manifest" "flux_slack_alert" {
  count      = local.flux_slack_enabled ? 1 : 0
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
      # app-flavor merges in conditionally so legacy manifests stay unchanged. beanstalkd-image
      # keys on the tag being set, not the flavor, since legacy envs can also run the dedicated
      # beanstalkd image now (chart_version >= 2.10.27).
      eventMetadata = merge(
        {
          env             = "${var.customer}-${var.environment}"
          chart           = var.chart_version
          app-image       = var.image_tag
          webnextjs-image = var.nextjs_tag
        },
        var.app_image_flavor == "slim" ? { app-flavor = "slim" } : {},
        var.beanstalkd_tag != "" ? { beanstalkd-image = var.beanstalkd_tag } : {},
      )
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
