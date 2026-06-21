# In-cluster Redis backing Envoy Gateway's GLOBAL rate-limit service.
#
# The rate-limit BackendTrafficPolicies shipped by the dozuki chart
# (gateway.rateLimit.*) only ENFORCE when the EnvoyGateway controller has a
# rate-limit backend configured — see the config.envoyGateway.rateLimit block
# on helm_release.envoy_gateway. Without this, those policies install but are
# inert. EnvoyGateway auto-provisions its envoy-ratelimit service once this
# backend is set and a rate-limit policy exists.
#
# Single-replica, in-memory (in-cluster default so MPC/ECP stays self-contained
# and air-gap friendly). HA Redis is a future enhancement.
#
# Security hardening (C2 — rate-limit Redis lockdown):
#   1. AUTH: random password stored in a K8s Secret; injected into Redis via
#      --requirepass and into the envoy-ratelimit pod via REDIS_AUTH env var
#      sourced from the same Secret (no plaintext in ConfigMap).
#   2. NetworkPolicy: ingress to redis-system/redis limited to pods in
#      envoy-gateway-system. Enforcement is CNI-dependent (EKS Auto Mode
#      enforces via VPC CNI network policies; docker-desktop may not).
#      NetworkPolicy is defense-in-depth; AUTH is the in-band control.
#   3. securityContext: non-root (uid 999), read-only root filesystem,
#      no privilege escalation, all capabilities dropped.
#   4. emptyDir /data + --save "" : no-persistence (in-memory) mode required
#      by readOnlyRootFilesystem; a counter reset on Redis restart is
#      acceptable for a rate-limit store.
#   5. CPU limit added to bound resource consumption.

# ---------------------------------------------------------------------------
# Auth secret
#
# The same password is written into two namespaces:
#   - redis-system         : read by the Redis pod via REDIS_PASSWORD env var
#                            (used in --requirepass $(REDIS_PASSWORD))
#   - envoy-gateway-system : read by the envoy-ratelimit pod via REDIS_AUTH env
#                            var (valueFrom.secretKeyRef in rateLimitDeployment)
#
# K8s secretKeyRef cannot cross namespaces, so we write the same secret to
# both. The random_password resource generates a single value; both secrets
# reference it, so they are always in sync.
# ---------------------------------------------------------------------------

resource "random_password" "redis_auth" {
  length  = 32
  special = false # avoid chars that complicate shell quoting in --requirepass
}

# Secret in redis-system: consumed by the Redis Deployment (REDIS_PASSWORD).
resource "kubernetes_secret_v1" "redis_auth" {
  metadata {
    name      = "redis-auth"
    namespace = kubernetes_namespace_v1.ratelimit_redis.metadata[0].name
  }

  data = {
    password = random_password.redis_auth.result
  }

  type = "Opaque"
}

# Secret in envoy-gateway-system: consumed by the envoy-ratelimit Deployment
# (REDIS_AUTH), injected via rateLimitDeployment.container.env in the EG config.
# EG manages the ratelimit Deployment in its own namespace, so the secret must
# live there for valueFrom.secretKeyRef to resolve.
#
# The envoy-gateway-system namespace is created by helm_release.envoy_gateway
# (create_namespace = true). EG provisions the envoy-ratelimit Deployment lazily
# — only after a Gateway + rate-limit BackendTrafficPolicy exists (both come from
# helm_release.app, later). So this secret just needs to exist before app installs;
# depends_on = [helm_release.envoy_gateway] ensures the namespace is present first.
resource "kubernetes_secret_v1" "redis_auth_eg" {
  metadata {
    name      = "redis-auth"
    namespace = "envoy-gateway-system"
  }

  data = {
    password = random_password.redis_auth.result
  }

  type = "Opaque"

  depends_on = [helm_release.envoy_gateway]
}

# ---------------------------------------------------------------------------
# Namespace
# ---------------------------------------------------------------------------

resource "kubernetes_namespace_v1" "ratelimit_redis" {
  metadata {
    name = "redis-system"
  }
}

# ---------------------------------------------------------------------------
# Deployment
# ---------------------------------------------------------------------------

resource "kubernetes_deployment_v1" "ratelimit_redis" {
  metadata {
    name      = "redis"
    namespace = kubernetes_namespace_v1.ratelimit_redis.metadata[0].name
    labels    = { app = "redis" }
  }
  spec {
    replicas = 1
    selector {
      match_labels = { app = "redis" }
    }
    template {
      metadata {
        labels = { app = "redis" }
      }
      spec {
        # Run as the redis user (uid 999) — matches the redis:7-alpine image default.
        security_context {
          run_as_non_root = true
          run_as_user     = 999
          run_as_group    = 999
          seccomp_profile {
            type = "RuntimeDefault"
          }
        }

        container {
          name  = "redis"
          image = "redis:7-alpine"

          # --save "" disables persistence (in-memory only).
          # --requirepass sources the password from the env var below.
          # $(REDIS_PASSWORD) is Kubernetes' own container env-var substitution
          # in args (exec form) — not shell expansion.
          command = ["redis-server"]
          args = [
            "--save", "",
            "--maxmemory", "64mb",
            "--maxmemory-policy", "allkeys-lru",
            "--requirepass", "$(REDIS_PASSWORD)",
          ]

          env {
            name = "REDIS_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.redis_auth.metadata[0].name
                key  = "password"
              }
            }
          }

          port {
            container_port = 6379
          }

          security_context {
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            run_as_non_root            = true
            run_as_user                = 999
            capabilities {
              drop = ["ALL"]
            }
          }

          volume_mount {
            name       = "data"
            mount_path = "/data"
          }

          resources {
            requests = {
              cpu    = "25m"
              memory = "32Mi"
            }
            limits = {
              cpu    = "100m"
              memory = "96Mi"
            }
          }
        }

        volume {
          name = "data"
          empty_dir {}
        }
      }
    }
  }
}

# ---------------------------------------------------------------------------
# Service
# ---------------------------------------------------------------------------

resource "kubernetes_service_v1" "ratelimit_redis" {
  metadata {
    name      = "redis"
    namespace = kubernetes_namespace_v1.ratelimit_redis.metadata[0].name
  }
  spec {
    selector = { app = "redis" }
    port {
      port        = 6379
      target_port = 6379
    }
  }
}

# ---------------------------------------------------------------------------
# NetworkPolicy
#
# Allows ingress to TCP 6379 ONLY from pods in envoy-gateway-system.
# All other ingress to the redis pod is denied by the absence of a matching
# ingress rule (default-deny for selected pods).
#
# NOTE: NetworkPolicy enforcement is CNI-dependent. EKS Auto Mode (AWS VPC CNI
# with network policy support) enforces this. docker-desktop's built-in CNI
# may silently ignore NetworkPolicy; test there is only for apply/syntax
# validation. Auth (--requirepass / REDIS_AUTH) is the primary in-band control.
# ---------------------------------------------------------------------------

resource "kubernetes_network_policy_v1" "ratelimit_redis" {
  metadata {
    name      = "redis-allow-ratelimit"
    namespace = kubernetes_namespace_v1.ratelimit_redis.metadata[0].name
  }

  spec {
    pod_selector {
      match_labels = { app = "redis" }
    }

    policy_types = ["Ingress"]

    ingress {
      from {
        namespace_selector {
          match_labels = {
            "kubernetes.io/metadata.name" = "envoy-gateway-system"
          }
        }
      }
      ports {
        protocol = "TCP"
        port     = "6379"
      }
    }
  }
}
