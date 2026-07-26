# ---------------------------------------------------------------------------
# Values diff view: the dozuki-flux-values-diff ConfigMap
#
# The app values ship to Flux inside a Secret (flux.tf), so any values change
# plans as one opaque "(sensitive value)" line - the per-key diffs we had from
# helm_release set blocks disappeared in the Flux move. This ConfigMap restores
# them: the same values tree flattened to one dotted-path entry per leaf, so a
# plan renders exactly the keys that changed:
#
#   ~ "webhooks.enabled" = "false" -> "true"
#
# NOTHING consumes it - it exists purely so plans (and the PR diff comments
# built from them) show what changed in the values. Leaves are jsonencoded
# (strings keep their quotes) so string-vs-bool stays visible; helm type
# coercion has bitten before (useSSL, redis.tls).
#
# Leaves carrying a tofu sensitivity mark are stored as a short sha256, so a
# rotation is visible without the value. That protection rides entirely on the
# marks: keep secret-bearing variables declared sensitive = true.
#
# HCL has no recursion, so the flatten is an unrolled chain sized for the
# values tree (10 levels; the deepest real path today is 6). A deeper leaf
# degrades to a JSON-blob entry, which still diffs.
# ---------------------------------------------------------------------------

locals {
  vd0 = local.app_values
  vd1 = merge(
    merge([for k, v in local.vd0 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd0 : k => v if !can(keys(v)) },
  )
  vd2 = merge(
    merge([for k, v in local.vd1 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd1 : k => v if !can(keys(v)) },
  )
  vd3 = merge(
    merge([for k, v in local.vd2 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd2 : k => v if !can(keys(v)) },
  )
  vd4 = merge(
    merge([for k, v in local.vd3 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd3 : k => v if !can(keys(v)) },
  )
  vd5 = merge(
    merge([for k, v in local.vd4 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd4 : k => v if !can(keys(v)) },
  )
  vd6 = merge(
    merge([for k, v in local.vd5 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd5 : k => v if !can(keys(v)) },
  )
  vd7 = merge(
    merge([for k, v in local.vd6 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd6 : k => v if !can(keys(v)) },
  )
  vd8 = merge(
    merge([for k, v in local.vd7 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd7 : k => v if !can(keys(v)) },
  )
  vd9 = merge(
    merge([for k, v in local.vd8 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd8 : k => v if !can(keys(v)) },
  )
  vd10 = merge(
    merge([for k, v in local.vd9 : { for k2, v2 in v : "${k}.${k2}" => v2 } if can(keys(v))]...),
    { for k, v in local.vd9 : k => v if !can(keys(v)) },
  )

  # Leaves as strings. jsonencode on purpose (not tostring): quoted strings
  # keep scalar types distinguishable.
  vd_encoded = { for k, v in local.vd10 : k => jsonencode(v) }

  # Sensitivity-marked leaves become short hashes. if-filtered comprehensions
  # on purpose: a ternary whose condition touches a sensitive value re-marks
  # its result, and the entry renders "(sensitive value)" again.
  vd_redacted = merge(
    { for k, v in local.vd_encoded : k => "sha256:${substr(sha256(nonsensitive(v)), 0, 12)}" if issensitive(v) },
    { for k, v in local.vd_encoded : k => v if !issensitive(v) },
  )

  # ConfigMap keys only allow [-._a-zA-Z0-9]. Sanitize, and tolerate a
  # post-sanitize collision by keeping the first entry (view-only data).
  vd_grouped       = { for k, v in local.vd_redacted : replace(k, "/[^-._a-zA-Z0-9]/", "_") => v... }
  values_diff_view = { for k, vl in local.vd_grouped : k => vl[0] }
}

resource "kubernetes_config_map_v1" "flux_values_diff" {
  metadata {
    name      = "dozuki-flux-values-diff"
    namespace = kubernetes_namespace_v1.flux_system.metadata[0].name
    labels    = { "app.kubernetes.io/managed-by" = "terraform" }
  }

  data = local.values_diff_view
}
