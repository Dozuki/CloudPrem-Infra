data "aws_ssm_parameter" "dozuki_customer_id" {
  name = local.dozuki_customer_id_parameter_name
}
data "aws_ssm_parameter" "nlb_ssl_cert" {
  name = var.nlb_ssl_server_cert_parameter
}
data "aws_ssm_parameter" "nlb_ssl_key" {
  name = var.nlb_ssl_server_key_parameter
}

resource "random_password" "dashboard_password" {
  length  = 16
  special = true

  keepers = {
    nlb_dns_name = var.nlb_dns_name
  }
}

resource "null_resource" "pull_replicated_license" {

  triggers = {
    customer_parameter_name = data.aws_ssm_parameter.dozuki_customer_id.value
    # We use a timestamp as a trigger so this resource is always executed. That way the license file is always available
    # in case of a partial apply.
    timestamp = timestamp()
  }

  provisioner "local-exec" {
    command = "curl -o dozuki.yaml -H 'Authorization: ${self.triggers.customer_parameter_name}' https://replicated.app/customer/license/download/dozukikots"
  }
}

resource "local_file" "replicated_bootstrap_config" {
  filename = "./replicated_config.yaml"
  content  = <<EOT
apiVersion: kots.io/v1beta1
kind: ConfigValues
metadata:
  name: replicated_bootstrap_config
spec:
  values:
    hostname:
      value: ${var.nlb_dns_name}
    tls_private_key_file:
      value: ${base64encode(data.aws_ssm_parameter.nlb_ssl_key.value)}
    tls_certificate_file:
      value: ${base64encode(data.aws_ssm_parameter.nlb_ssl_cert.value)}
status: {}
EOT
}


resource "local_file" "replicated_install" {
  depends_on = [null_resource.pull_replicated_license]

  filename = "./kots_install.sh"
  content  = <<EOT
#!/bin/bash
set -euo pipefail

${local.aws_profile_prefix} aws --region ${data.aws_region.current.name} eks update-kubeconfig --name ${var.eks_cluster_id} --role-arn ${var.eks_cluster_access_role_arn}

kubectl config set-context --current --namespace=${kubernetes_namespace.kots_app.metadata[0].name}

[[ -x $(which kubectl-kots) ]] || curl https://kots.io/install | bash

set -v

kubectl kots install ${local.app_and_channel} \
  --namespace ${kubernetes_namespace.kots_app.metadata[0].name} \
  --license-file ./dozuki.yaml \
  --shared-password '${random_password.dashboard_password.result}' \
  --config-values ${local_file.replicated_bootstrap_config.filename} \
  --no-port-forward \
  --skip-preflights \
  --wait-duration=10m


EOT
  provisioner "local-exec" {
    command = "./kots_install.sh"
  }
}