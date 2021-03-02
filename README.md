# Dozuki Cloudprem infrastructure

![Terraform](https://github.com/nclouds/doz-cloudprem-infrastructure/workflows/Terraform/badge.svg)

The Terraform project automates the creation of AWS resources as well as some base Kubernetes components required for the Cloudprem application infrastructure.

![dozuki](https://app.lucidchart.com/publicSegments/view/c01199f1-8171-415f-b3ca-09206a593da5/image.png)

## Deployment

The infrastructure is managed and deployed using [Terragrunt](https://terragrunt.gruntwork.io/docs/#features). By using terragrunt we are able to deploy the infrastructure to multiple environments, lock and version the infrastructure and keep Terraform code and state configuration DRY.

The terragrunt configurations for each environment can be found in the [live](./live) directory. The outer [terragrunt.hcl](./live/terragrunt.hcl) file contains configurations for the backend state and locks. The [terragrunt.hcl](./live/development) files under each environment directory contain the parameters for that specific environment and the location of the Terraform stack. *(Note: For the Cloudprem infrastructure development, local references to the modules are used, eg. `../..//cloudprem`. For the customers deployment a reference to a specific version of the stack should be declared, eg. `git::https://github.com/dozuki/cloudprem-infrastructure.git//cloudprem?ref=v0.0.1`)*

To deploy the stack, perform the following steps:

1. Initialize the backend and install the required providers and modules using terragrunt:

    ```console
    $ cd live/development
    $ terragrun init
    ```

2. Review the parameters in the [terragrunt.hcl](./live/development/terragrunt.hcl) file and execute the plan/apply

    ```console
    $ terragrunt apply
    ```

To delete the infrastructure for the environment execute terragrunt destroy

```console
$ terragrunt destroy
```

## Contributing

To contribute to the project, make your changes to the Terraform code and deploy them to a local environment using the parameters in the [live/development](./live/development) directory by following the instructions in the [deploy](#deploy) section.

### Pre-commit

The project is configured with [pre-commit](https://pre-commit.com/) hooks to perform checks on the code and provide faster feedback. Consider using it on your development workflow.

To enable the pre-commit hooks:

```console
$ pre-commit install
```

The checks are going to be executed by default on every commit, if you want to perform the checks manually execute:

```console
$ pre-commit run -a
```

For more information about the pre-commit hooks configured check the [pre-commit configuration repository](https://github.com/nclouds/pre-commit-terraform)

### CI / CD

The repository is configured with two Github Workflows:

1. Terraform: Performs validations using (terraform fmt, tflint and tfsec) for quality compliance. This CI pipeline is executed on every commit pushed github.
2. Release: This workflow creates a github release of the Terraform project whenever a new tag of the form v* (i.e. v1.0, v20.15.10) is pushed to the repository. To control the Release notes and properties update the [release.yml](./.github/workflows/release.yml) file.

#### Additional considerations

By default only the user that creates the EKS cluster has permissions to access the cluster, for that reason if you create the Terraform stack with the pipeline and then try to update the stack manually you'll get an `Unauthorized` error when Terraform attempts to update or refresh the state of the kubernetes resources. To overcome that a role called *deployment_role* is created as part of the pipeline and used to deploy the infrastructure.

To perform manual updates to the infrastructure after deploying it with the pipeline get the deployment role arn from the CloudFormation pipeline and assume the role:

```console
aws_credentials=$(aws sts assume-role --role-arn <deployment_role_arn> --role-session-name "Terraform")
export AWS_ACCESS_KEY_ID=$(echo $aws_credentials|jq '.Credentials.AccessKeyId'|tr -d '"')
export AWS_SECRET_ACCESS_KEY=$(echo $aws_credentials|jq '.Credentials.SecretAccessKey'|tr -d '"')
export AWS_SESSION_TOKEN=$(echo $aws_credentials|jq '.Credentials.SessionToken'|tr -d '"')
terragrunt apply
```

Note that the role session name is Terraform, you must use the exact same session name to perform updates.