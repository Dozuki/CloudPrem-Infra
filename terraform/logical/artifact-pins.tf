# Plan-time existence checks for the pinned artifacts (app image, web-nextjs
# image, chart). A tag that was never pushed to the env's registry used to
# surface as ImagePullBackOff 20 minutes into the apply, wedging the
# HelmRelease until a human intervened (seen live 2026-07-30: image_tag
# pointed at a build that was never mirrored to the gov ECR `app` repo; the
# helm wait then sat on a stalled Deployment). These data sources make the
# same mistake fail the PLAN in seconds with a readable ImageNotFound error.
#
# Cross-account note: DescribeImages needs the registry's per-repo resource
# policy to allow the consumer account (verified live: workload accounts can
# describe app/web-nextjs/charts-dozuki in both the commercial build registry
# and the gov mirror). If a registry ever revokes it, set
# verify_artifact_pins = false rather than shipping unverified pins blind.
#
# Scoped to ECR registries only: Azure/GHCR-backed stacks have no ECR API to
# ask, and their registries reject nothing at plan time; they keep the old
# behavior.

locals {
  # The registry account, parsed from the configured ECR registry hostname.
  # null for non-ECR hosts (GHCR on Azure/connected installs), which disables
  # the checks. Region comes from the same host, like the chart token above.
  pin_registry_id = try(regex("^(\\d{12})\\.dkr\\.ecr", var.image_repository)[0], null)
  pin_ecr_region  = try(regex("\\.ecr\\.([a-z0-9-]+)\\.amazonaws\\.com", var.image_repository)[0], null)
  verify_ecr_pins = var.verify_artifact_pins && var.cloud == "aws" && local.pin_registry_id != null
}

data "aws_ecr_image" "app_pin" {
  count           = local.verify_ecr_pins && var.image_tag != "" ? 1 : 0
  region          = local.pin_ecr_region
  registry_id     = local.pin_registry_id
  repository_name = var.app_image_flavor == "slim" ? "monolith-app" : "app"
  image_tag       = var.image_tag
}

data "aws_ecr_image" "beanstalkd_pin" {
  count           = local.verify_ecr_pins && var.app_image_flavor == "slim" && var.beanstalkd_tag != "" ? 1 : 0
  region          = local.pin_ecr_region
  registry_id     = local.pin_registry_id
  repository_name = "beanstalkd"
  image_tag       = var.beanstalkd_tag
}

data "aws_ecr_image" "nextjs_pin" {
  count           = local.verify_ecr_pins && var.nextjs_tag != "" ? 1 : 0
  region          = local.pin_ecr_region
  registry_id     = local.pin_registry_id
  repository_name = "web-nextjs"
  image_tag       = var.nextjs_tag
}

data "aws_ecr_image" "chart_pin" {
  count           = local.verify_ecr_pins && var.chart_version != "" ? 1 : 0
  region          = local.pin_ecr_region
  registry_id     = local.pin_registry_id
  repository_name = "charts/dozuki"
  image_tag       = var.chart_version
}
