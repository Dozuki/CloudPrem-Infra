data "aws_ssm_parameter" "dozuki_license" {
  name = var.dozuki_license_parameter_name
}

resource "kubernetes_secret" "replicated_license" {
  metadata {
    name = "replicated-license"

    labels = {
      project   = "replicated"
      terraform = "true"
    }
  }

  data = {
    "license.rli" = data.aws_ssm_parameter.dozuki_license.value
  }
}