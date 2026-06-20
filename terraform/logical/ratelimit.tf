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

resource "kubernetes_namespace_v1" "ratelimit_redis" {
  metadata {
    name = "redis-system"
  }
}

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
        container {
          name  = "redis"
          image = "redis:7-alpine"
          args  = ["--maxmemory", "64mb", "--maxmemory-policy", "allkeys-lru"]
          port {
            container_port = 6379
          }
          resources {
            requests = {
              cpu    = "25m"
              memory = "32Mi"
            }
            limits = {
              memory = "96Mi"
            }
          }
        }
      }
    }
  }
}

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
