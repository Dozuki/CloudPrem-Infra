# external-dns on AKS (azure only): assumes an AWS IAM role via AKS workload
# identity (projected token, audience sts.amazonaws.com) and publishes the
# dozuki.cloud record for the gateway into Route53. Enabled when an AWS role ARN
# is provided. AWS is unaffected (count = 0).

locals {
  external_dns_enabled = var.cloud == "azure" && var.aws_external_dns_role_arn != ""
  azure_region         = var.cloud == "azure" ? data.azurerm_kubernetes_cluster.main[0].location : ""
  lb_fqdn              = var.gateway_dns_label != "" ? "${var.gateway_dns_label}.${local.azure_region}.cloudapp.azure.com" : ""
}

resource "helm_release" "external_dns" {
  count = local.external_dns_enabled ? 1 : 0

  name       = "external-dns"
  namespace  = kubernetes_namespace_v1.app.metadata[0].name
  repository = "https://kubernetes-sigs.github.io/external-dns/"
  chart      = "external-dns"
  version    = "1.15.2"
  wait       = true
  timeout    = 300

  # Use the chart's native value keys (it generates --registry/--policy/--source/
  # --domain-filter/--txt-owner-id/--aws-zone-type itself). Do NOT also pass these
  # via extraArgs — the chart already emits --registry by default, so duplicating
  # any of them is a fatal "flag cannot be repeated".
  values = [yamlencode({
    provider      = { name = "aws" }
    sources       = ["gateway-httproute"]
    domainFilters = ["dozuki.cloud"]
    policy        = "sync"
    registry      = "txt"
    txtOwnerId    = "azure-mpc-${var.environment}"
    # Prefix ownership TXT records so they never sit at the same name as a managed
    # CNAME (a TXT and CNAME can't coexist at one name — Route53 rejects the batch).
    txtPrefix = "edns-"
    aws       = { zoneType = "public" }

    serviceAccount = {
      create = true
      name   = var.external_dns_sa_name
    }

    extraVolumes = [{
      name = "aws-token"
      projected = {
        sources = [{
          serviceAccountToken = {
            audience          = "sts.amazonaws.com"
            expirationSeconds = 3600
            path              = "token"
          }
        }]
      }
    }]
    extraVolumeMounts = [{
      name      = "aws-token"
      mountPath = "/var/run/secrets/aws"
      readOnly  = true
    }]
    env = [
      { name = "AWS_ROLE_ARN", value = var.aws_external_dns_role_arn },
      { name = "AWS_WEB_IDENTITY_TOKEN_FILE", value = "/var/run/secrets/aws/token" },
      { name = "AWS_REGION", value = "us-east-1" },
    ]
  })]
}
# NOTE: no azure.workload.identity label is needed — this federates to AWS, not
# Azure AD. The projected serviceAccountToken with audience sts.amazonaws.com is
# a standard Kubernetes feature (TokenRequest), independent of the Azure
# workload-identity webhook.
