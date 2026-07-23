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

  mesh_state_rank = { disabled = 0, installed = 1, permissive = 2, strict = 3 }
  mesh_rank       = local.mesh_state_rank[var.istio_mesh_state]

  # Platform contract: ambient is validated on commercial AWS EKS Auto Mode only.
  # Gov joins after the haul pipeline mirrors istio images/charts (phase 2). Azure
  # is deferred (no node-taint surface on AKS today; see the design annex).
  mesh_supported = var.cloud == "aws" && !local.is_us_gov

  mesh_installed = local.mesh_rank >= 1
  mesh_enrolled  = local.mesh_rank >= 2
  mesh_strict    = local.mesh_rank >= 3
}
