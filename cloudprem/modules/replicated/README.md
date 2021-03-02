# Dozuki replicated terraform module

A terraform module to install replicated in a Kubernetes cluster

## Module usage:

```hcl
module "replicated" {
  source = "./modules/replicated"

  dozuki_license_parameter_name = "/cloudprem-dev/license"
}
```

<!-- BEGINNING OF PRE-COMMIT-TERRAFORM DOCS HOOK -->
## Requirements

| Name | Version |
|------|---------|
| terraform | >= 0.14.0 |
| aws | >= 3.2.0 |
| helm | ~> 2.0.1 |
| kubernetes | ~> 1.13.3 |

## Providers

| Name | Version |
|------|---------|
| aws | >= 3.2.0 |
| helm | ~> 2.0.1 |
| kubernetes | ~> 1.13.3 |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| dozuki\_license\_parameter\_name | The SSM parameter name that stores the Dozuki license file provided to you. | `string` | n/a | yes |

## Outputs

No output.

<!-- END OF PRE-COMMIT-TERRAFORM DOCS HOOK -->

## Development

The module is based on the replicated kubernetes manifest generated with the `kubernetes-yml-generate` by following the [Replicated documentation](https://help.replicated.com/docs/kubernetes/customer-installations/existing-cluster/). A simple Helm chart was created to be deployed within Terraform with the Helm provider. To update replicated to a newer version replace the templates in the Helm chart with the updated kubernetes manifests. We created a simple [bash script](./update_replicated_installer.sh) that can be used as guidance, however we recommend to review the documentation and the script before upgrading.

```console
$ ./update_replicated_installer.sh
```