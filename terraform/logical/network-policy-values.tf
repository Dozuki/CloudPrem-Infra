# Chart values for the web-nextjs NetworkPolicy and puppeteer's egress deny list.
#
# The deny list is the env VPC CIDR(s) + the EKS Service CIDR + link-local. The chart REPLACES
# its default list with this one.
#
# The policy denies the env VPC and Service CIDR. If CREATOR_PRO/MARKETPLACE/DOZUKI_SERVICES/
# CHAT_SERVICE_API_URL is later pointed at an in-cluster Service or an in-VPC address via
# nextjs_extra_env, web-nextjs will time out on it with no policy error; the fix is a
# podSelector rule in the chart, not widening the CIDR.
#
# Below the chart floor no key is emitted at all (never null), so flux values stay
# byte-identical and no HelmRelease revision happens.

data "aws_vpc" "cluster" {
  count = var.cloud == "aws" ? 1 : 0
  id    = data.aws_eks_cluster.main[0].vpc_config[0].vpc_id
}

locals {
  env_vpc_cidrs       = [for a in try(one(data.aws_vpc.cluster[*]).cidr_block_associations, []) : a.cidr_block] # includes the primary
  env_service_cidr    = try(one(data.aws_eks_cluster.main[*]).kubernetes_network_config[0].service_ipv4_cidr, null)
  netpol_denied_cidrs = compact(concat(local.env_vpc_cidrs, [local.env_service_cidr, "169.254.0.0/16"]))

  # Chart floor: 3.20.0 carries the shared NetworkPolicy helper and the web-nextjs policy.
  netpol_chart_ok = var.cloud == "aws" && try(
    length(local.chart_version_core) == 3 && (
      tonumber(local.chart_version_core[0]) >= 4 ||
      (tonumber(local.chart_version_core[0]) == 3 && tonumber(local.chart_version_core[1]) >= 20)
    ), false
  )

  # for-if instead of a ternary: the two arms would have different object types.
  netpol_values = {
    for k, v in {
      webNextjs = { networkPolicy = { enabled = true, deniedCidrs = local.netpol_denied_cidrs } }
      puppeteer = { networkPolicy = { deniedCidrs = local.netpol_denied_cidrs } }
    } : k => v if local.netpol_chart_ok
  }
}
