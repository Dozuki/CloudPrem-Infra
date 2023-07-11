data "aws_ssm_parameter" "dozuki_customer_id" {
  name = local.dozuki_customer_id_parameter_name
}

resource "random_password" "dashboard_password" {
  length  = 16
  special = true
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
  content = yamlencode({
    apiVersion = "kots.io/v1beta1"
    kind       = "ConfigValues"
    metadata = {
      name = "replicated_bootstrap_config"
    }
    spec = {
      values = local.all_config_values
    }
    status = {}
  })
}

# We create the ingress for the dashboard in terraform to ensure that even if the app deploy fails, we can still access the dashboard.
resource "kubernetes_ingress_v1" "dash" {
  depends_on = [helm_release.cert_manager]

  metadata {
    name      = "dash-tf"
    namespace = local.k8s_namespace_name
    annotations = {
      "cert-manager.io/cluster-issuer"                 = "cert-issuer"
      "nginx.ingress.kubernetes.io/ssl-redirect"       = "true"
      "nginx.ingress.kubernetes.io/force-ssl-redirect" = "true"
      "nginx.ingress.kubernetes.io/rewrite-target"     = "/"
    }
  }

  spec {
    ingress_class_name = "nginx-dash"
    rule {
      host = var.dns_domain_name

      http {
        path {
          path      = "/"
          path_type = "Prefix"
          backend {
            service {
              name = "kotsadm"
              port {
                number = 3000
              }
            }
          }
        }
      }
    }

    tls {
      hosts       = [var.dns_domain_name]
      secret_name = "tls-secret"
    }
  }
}


resource "local_file" "replicated_install" {
  depends_on = [null_resource.pull_replicated_license, helm_release.cert_manager]

  filename = "./kots_install.sh"
  content  = <<EOT
#!/usr/bin/env bash
set -euo pipefail

${local.aws_profile_prefix} aws --region ${data.aws_region.current.name} eks update-kubeconfig --name ${var.eks_cluster_id} --role-arn ${var.eks_cluster_access_role_arn}

kubectl config set-context --current --namespace=${kubernetes_namespace.kots_app.metadata[0].name}

chmod 755 ./vendor/kots-install.sh

[[ -x $(which kubectl-kots) ]] || ./vendor/kots-install.sh

set -v

# Ensure the app is not already installed
if ! kubectl kots get apps -n ${kubernetes_namespace.kots_app.metadata[0].name} >/dev/null 2>&1; then

  kubectl kots install ${local.app_and_channel} \
    --namespace ${kubernetes_namespace.kots_app.metadata[0].name} \
    --license-file ./dozuki.yaml \
    --shared-password '${random_password.dashboard_password.result}' \
    --config-values ${local_file.replicated_bootstrap_config.filename} \
    --no-port-forward \
    --skip-preflights \
    --wait-duration=10m
else
  # If app is already installed, update the config with any changed values from this run.
  kubectl kots set config ${local.app_slug} --merge --config-file ${local_file.replicated_bootstrap_config.filename}
fi

EOT
  provisioner "local-exec" {
    command = "./kots_install.sh"
  }
}