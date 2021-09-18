# CloudPrem Infrastructure

This folder houses the terraform modules for the Cloudprem infrastructure.

The terraform stack is composed of many of the [open source AWS modules](https://registry.terraform.io/namespaces/terraform-aws-modules) and some custom modules.

![dozuki](https://app.lucidchart.com/publicSegments/view/c01199f1-8171-415f-b3ca-09206a593da5/image.png)

<!-- BEGINNING OF PRE-COMMIT-TERRAFORM DOCS HOOK -->
## Requirements

| Name | Version |
|------|---------|
| terraform | ~> 1.0.6 |
| terragrunt | ~> 0.31.8 |

## Terraform Documentation
Our CloudPrem infrastructure is built with 4 terraform modules. Information about
the inputs, outputs, and resources of each are included in the README files linked below:

1. [Network Module](./network/README.md)
2. [Compute Module](./compute/README.md)
3. [Storage Module](./storage/README.md)
4. [App Module](./app/README.md)

This is also the execution order when running `terragrunt run-all` commands.
