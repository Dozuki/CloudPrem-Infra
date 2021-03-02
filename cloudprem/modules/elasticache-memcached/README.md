# Elasticache Memcached terraform module

A terraform module to create an Elasticache Memached cluster

## Module usage:

```hcl
module "memcached" {
  source = "./modules/elasticache-memcached"

  name = local.identifier

  vpc_id     = local.vpc_id
  subnet_ids = local.private_subnet_ids

  allowed_cidr_blocks = [local.vpc_cidr]

  cluster_size  = 1
  instance_type = var.cache_instance_type
}
```

<!-- BEGINNING OF PRE-COMMIT-TERRAFORM DOCS HOOK -->
## Requirements

| Name | Version |
|------|---------|
| terraform | >= 0.13.0 |
| aws | >= 3.2.0 |
| null | ~> 2.0 |

## Providers

| Name | Version |
|------|---------|
| aws | >= 3.2.0 |
| null | ~> 2.0 |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| allowed\_cidr\_blocks | List of CIDR blocks that are allowed ingress to the cluster's Security Group created in the module | `list(string)` | `[]` | no |
| allowed\_security\_groups | List of Security Group IDs that are allowed ingress to the cluster's Security Group created in the module | `list(string)` | `[]` | no |
| cluster\_size | Cluster size | `number` | `1` | no |
| create\_security\_group | Flag to enable/disable creation of Security Group in the module. | `bool` | `true` | no |
| elasticache\_parameter\_group\_family | ElastiCache parameter group family | `string` | `"memcached1.5"` | no |
| elasticache\_subnet\_group\_name | Subnet group name for the ElastiCache instance | `string` | `""` | no |
| engine\_version | Memcached engine version. For more info, see https://docs.aws.amazon.com/AmazonElastiCache/latest/mem-ug/supported-engine-versions.html | `string` | `"1.5.16"` | no |
| existing\_security\_groups | List of existing Security Group IDs to place the cluster into. Set `use_existing_security_groups` to `true` to enable using `existing_security_groups` as Security Groups for the cluster | `list(string)` | `[]` | no |
| instance\_type | Elastic cache instance type | `string` | `"cache.t2.micro"` | no |
| name | Name of the application | `string` | n/a | yes |
| port | Memcached port | `number` | `11211` | no |
| subnet\_ids | AWS subnet ids | `list(string)` | `[]` | no |
| tags | Additional tags (\_e.g.\_ map("BusinessUnit","ABC") | `map(string)` | `{}` | no |
| vpc\_id | VPC ID | `string` | `""` | no |

## Outputs

| Name | Description |
|------|-------------|
| cluster\_address | Cluster address |
| cluster\_configuration\_endpoint | Cluster configuration endpoint |
| cluster\_id | Cluster ID |
| cluster\_urls | Cluster URLs |
| port | Cluster endpoint port |
| security\_group\_id | Security Group ID |

<!-- END OF PRE-COMMIT-TERRAFORM DOCS HOOK -->
