<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | 3.56.0 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | 3.56.0 |
| <a name="provider_helm"></a> [helm](#provider\_helm) | n/a |

## Modules

| Name | Source | Version |
|------|--------|---------|
| <a name="module_aws_node_termination_handler_role"></a> [aws\_node\_termination\_handler\_role](#module\_aws\_node\_termination\_handler\_role) | terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc | 4.3.0 |
| <a name="module_aws_node_termination_handler_sqs"></a> [aws\_node\_termination\_handler\_sqs](#module\_aws\_node\_termination\_handler\_sqs) | terraform-aws-modules/sqs/aws | ~> 3.1.0 |
| <a name="module_cluster_access_role"></a> [cluster\_access\_role](#module\_cluster\_access\_role) | terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc | 4.3.0 |
| <a name="module_cluster_access_role_assumable"></a> [cluster\_access\_role\_assumable](#module\_cluster\_access\_role\_assumable) | terraform-aws-modules/iam/aws//modules/iam-assumable-role | 4.3.0 |
| <a name="module_cpu_alarm"></a> [cpu\_alarm](#module\_cpu\_alarm) | terraform-aws-modules/cloudwatch/aws//modules/metric-alarm | 2.1.0 |
| <a name="module_eks_cluster"></a> [eks\_cluster](#module\_eks\_cluster) | terraform-aws-modules/eks/aws | 17.20.0 |
| <a name="module_memory_alarm"></a> [memory\_alarm](#module\_memory\_alarm) | terraform-aws-modules/cloudwatch/aws//modules/metric-alarm | 2.1.0 |
| <a name="module_nlb"></a> [nlb](#module\_nlb) | terraform-aws-modules/alb/aws | 6.5.0 |
| <a name="module_nodes_alarm"></a> [nodes\_alarm](#module\_nodes\_alarm) | terraform-aws-modules/cloudwatch/aws//modules/metric-alarm | 2.1.0 |
| <a name="module_sns"></a> [sns](#module\_sns) | terraform-aws-modules/sns/aws | 2.1.0 |
| <a name="module_status_alarm"></a> [status\_alarm](#module\_status\_alarm) | terraform-aws-modules/cloudwatch/aws//modules/metric-alarm | 2.1.0 |

## Resources

| Name | Type |
|------|------|
| [aws_autoscaling_lifecycle_hook.aws_node_termination_handler](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/autoscaling_lifecycle_hook) | resource |
| [aws_cloudwatch_event_rule.aws_node_termination_handler_asg](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/cloudwatch_event_rule) | resource |
| [aws_cloudwatch_event_rule.aws_node_termination_handler_spot](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/cloudwatch_event_rule) | resource |
| [aws_cloudwatch_event_target.aws_node_termination_handler_asg](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/cloudwatch_event_target) | resource |
| [aws_cloudwatch_event_target.aws_node_termination_handler_spot](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/cloudwatch_event_target) | resource |
| [aws_iam_policy.aws_node_termination_handler](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/iam_policy) | resource |
| [aws_iam_policy.cluster_access](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/iam_policy) | resource |
| [aws_iam_policy.cluster_autoscaler](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/iam_policy) | resource |
| [aws_iam_policy.eks_worker](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/iam_policy) | resource |
| [aws_security_group_rule.app_access_http](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.app_access_https](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/security_group_rule) | resource |
| [aws_security_group_rule.replicated_ui_access](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/resources/security_group_rule) | resource |
| [helm_release.aws_node_termination_handler](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.cluster_autoscaler](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [helm_release.metrics_server](https://registry.terraform.io/providers/hashicorp/helm/latest/docs/resources/release) | resource |
| [aws_caller_identity.current](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/caller_identity) | data source |
| [aws_eks_cluster.main](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/eks_cluster) | data source |
| [aws_eks_cluster_auth.main](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/eks_cluster_auth) | data source |
| [aws_iam_policy_document.aws_node_termination_handler](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.aws_node_termination_handler_events](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.cluster_access](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.cluster_autoscaler](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.eks_worker](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/iam_policy_document) | data source |
| [aws_kms_key.s3](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/kms_key) | data source |
| [aws_partition.current](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/partition) | data source |
| [aws_region.current](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/region) | data source |
| [aws_subnets.private](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/subnets) | data source |
| [aws_subnets.public](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/subnets) | data source |
| [aws_vpc.main](https://registry.terraform.io/providers/hashicorp/aws/3.56.0/docs/data-sources/vpc) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_app_access_cidr"></a> [app\_access\_cidr](#input\_app\_access\_cidr) | This CIDR will be allowed to connect to Dozuki. If running a public site, use the default value. Otherwise you probably want to lock this down to the VPC or your VPN CIDR. | `string` | `"0.0.0.0/0"` | no |
| <a name="input_eks_desired_capacity"></a> [eks\_desired\_capacity](#input\_eks\_desired\_capacity) | This is what the node count will start out as. | `number` | `"3"` | no |
| <a name="input_eks_instance_types"></a> [eks\_instance\_types](#input\_eks\_instance\_types) | The instance type of each node in the application's EKS worker node group. | `list(string)` | <pre>[<br>  "m5.large",<br>  "m5a.large",<br>  "m5d.large",<br>  "m5ad.large"<br>]</pre> | no |
| <a name="input_eks_max_size"></a> [eks\_max\_size](#input\_eks\_max\_size) | The maximum amount of nodes we will autoscale to. | `number` | `"10"` | no |
| <a name="input_eks_min_size"></a> [eks\_min\_size](#input\_eks\_min\_size) | The minimum amount of nodes we will autoscale to. | `number` | `"2"` | no |
| <a name="input_eks_volume_size"></a> [eks\_volume\_size](#input\_eks\_volume\_size) | The amount of local storage (in gigabytes) to allocate to each kubernetes node. Keep in mind you will be billed for this amount of storage multiplied by how many nodes you spin up (i.e. 50GB * 4 nodes = 200GB on your bill). For production installations 50GB should be the minimum. This local storage is used as a temporary holding area for uploaded and in-process assets like videos and images. | `number` | `50` | no |
| <a name="input_environment"></a> [environment](#input\_environment) | Environment of the application | `string` | `"dev"` | no |
| <a name="input_identifier"></a> [identifier](#input\_identifier) | A name identifier to use as prefix for all the resources. | `string` | `""` | no |
| <a name="input_kms_key_id"></a> [kms\_key\_id](#input\_kms\_key\_id) | AWS KMS key identifier for EKS encryption. The identifier can be one of the following format: Key id, key ARN, alias name or alias ARN | `string` | `"alias/aws/s3"` | no |
| <a name="input_protect_resources"></a> [protect\_resources](#input\_protect\_resources) | Specifies whether data protection settings are enabled. If true they will prevent stack deletion until protections have been manually disabled. | `bool` | `true` | no |
| <a name="input_public_access"></a> [public\_access](#input\_public\_access) | Should the app and dashboard be accessible via a publicly routable IP and domain? | `bool` | `true` | no |
| <a name="input_replicated_ui_access_cidr"></a> [replicated\_ui\_access\_cidr](#input\_replicated\_ui\_access\_cidr) | This CIDR will be allowed to connect to the app dashboard. This is where you upgrade to new versions as well as view cluster status and start/stop the cluster. You probably want to lock this down to your company network CIDR, especially if you chose 'true' for public access. | `string` | `"0.0.0.0/0"` | no |
| <a name="input_vpc_id"></a> [vpc\_id](#input\_vpc\_id) | The VPC ID where we'll be deploying our resources. (If creating a new VPC leave this field and subnets blank). When using an existing VPC be sure to tag at least 2 subnets with type = public and another 2 with tag type = private | `string` | `""` | no |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_cluster_primary_sg"></a> [cluster\_primary\_sg](#output\_cluster\_primary\_sg) | n/a |
| <a name="output_eks_cluster_access_role_arn"></a> [eks\_cluster\_access\_role\_arn](#output\_eks\_cluster\_access\_role\_arn) | n/a |
| <a name="output_eks_cluster_id"></a> [eks\_cluster\_id](#output\_eks\_cluster\_id) | n/a |
| <a name="output_nlb_dns_name"></a> [nlb\_dns\_name](#output\_nlb\_dns\_name) | n/a |
<!-- END_TF_DOCS -->