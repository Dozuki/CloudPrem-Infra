data "aws_ssm_parameter" "dozuki_license" {
  name = local.dozuki_license_parameter_name
}

resource "random_password" "dashboard_password" {
  length  = 16
  special = true

  keepers = {
    nlb_dns_name = var.nlb_dns_name
  }
}


resource "kubernetes_namespace" "kots_app" {
  metadata {
    name = local.k8s_namespace
  }
}

resource "local_file" "replicated_license" {
  filename = "./dozuki.yaml"
  content  = data.aws_ssm_parameter.dozuki_license.value
}

module "ssl_cert" {

  source      = "../common/acm"
  environment = var.environment
  identifier  = var.identifier

  cert_common_name = var.nlb_dns_name
  namespace        = local.k8s_namespace
}

resource "kubernetes_secret" "site_tls" {

  metadata {
    name      = "www-tls"
    namespace = local.k8s_namespace
  }

  data = {
    "onprem.key" = module.ssl_cert.ssm_server_key.value
    "onprem.crt" = module.ssl_cert.ssm_server_cert.value
  }
}


resource "local_file" "replicated_install" {
  depends_on = [local_file.replicated_license, kubernetes_secret.site_tls]

  filename = "./kots_install.sh"
  content  = <<EOT
#!/bin/sh
set -euo pipefail

aws --region ${data.aws_region.current.name} eks update-kubeconfig --name ${var.eks_cluster_id} --role-arn ${var.eks_cluster_access_role_arn}

kubectl config set-context --current --namespace=${local.k8s_namespace}

[[ -x $(which kubectl-kots) ]] || curl https://kots.io/install | bash

set -v

kubectl kots install ${local.app_and_channel} \
  --namespace ${local.k8s_namespace} \
  --license-file ./dozuki.yaml \
  --shared-password '${random_password.dashboard_password.result}' \
  --no-port-forward \
  --skip-preflights \
  --wait-duration=10m


EOT
  provisioner "local-exec" {
    command = "./kots_install.sh"
  }
}