# Dozuki CloudPrem Infrastructure

Infrastructure-as-code for deploying the **Dozuki** application as a managed private
cloud (MPC) — a self-contained, customer-isolated stack on **AWS** (commercial and
GovCloud) or **Azure**. The project provisions the cloud infrastructure, the
Kubernetes platform, and the application itself, and is driven by
[Terragrunt](https://terragrunt.gruntwork.io/) over Terraform / OpenTofu.

## Architecture

A deployment is split into two layers that apply in order. The split keeps the
expensive, slow-changing cloud infrastructure independent of the fast-moving
application/Kubernetes layer, and lets each layer use the toolchain it needs.

```
┌─────────────────────────────────────────────────────────────┐
│  logical   (terraform/logical)          Terraform 1.5.x       │
│  Kubernetes + Helm: cert-manager, metrics-server, External    │
│  Secrets Operator, Envoy Gateway, and the Dozuki app chart.   │
│  Seeds per-stack secrets into Vault.                          │
├─────────────────────────────────────────────────────────────┤
│  physical  (terraform/physical)         OpenTofu 1.11+        │
│  AWS: VPC, EKS (Auto Mode), RDS MySQL / Aurora Serverless v2, │
│  S3, ElastiCache, MSK, NLB, KMS, IAM, Route53, bastion,       │
│  cross-region DR, and a PrivateLink endpoint to central Vault.│
└─────────────────────────────────────────────────────────────┘
```

**Azure** deployments use a parallel `terraform/physical-azure` layer (AKS, Azure
Database for MySQL, VNet, Key Vault, managed identity) plus the same `logical`
layer, packaged with a self-contained deploy kit under [`azure-config/`](./azure-config).

Secrets are managed centrally in **HashiCorp Vault** (reached over PrivateLink and
surfaced into the cluster by the External Secrets Operator). The physical layer
provisions the Vault endpoint; the logical layer authenticates (AWS + Kubernetes
auth) and seeds each stack's secrets.

## Repository layout

```
terraform/
  physical/          AWS cloud infrastructure         (OpenTofu >= 1.11.1)
  logical/           Kubernetes / Helm / app          (Terraform >= 1.5.0)
  physical-azure/    Azure cloud infrastructure
live/                Terragrunt deployment configs
  terragrunt.hcl     Root: remote state, provider generation, input merge
  common.hcl         Shared inputs + logical→physical dependency wiring
  standard/          AWS commercial partition  (account.hcl → region → env)
  gov/               AWS GovCloud partition
  .skel/             Templates for scaffolding new partitions / environments
  tests/             Upgrade + DR integration harness (Go / Terratest)
azure-config/        Turnkey Azure deploy kit (image sync, charts, bootstrap)
.github/workflows/   CI: validation, integration tests, releases
DEPLOYMENT.md        Operator runbook for standing up / upgrading a stack
```

## The `live/` deployment model

Terragrunt configuration is layered so values flow down a hierarchy and merge into
each leaf module. The root [`live/terragrunt.hcl`](./live/terragrunt.hcl) generates
the S3 remote-state backend (with DynamoDB locking) and the provider block — the
primary `aws` provider plus the `aws.dns` (cross-account DNS) and `aws.dr`
(disaster-recovery region) aliases.

```
live/standard/                       partition
  account.hcl                        ← AWS account id + profile
  us-east-1/
    region.hcl                       ← region
    full/
      env.hcl                        ← feature flags for this environment
      physical/terragrunt.hcl        ← sources terraform/physical
      logical/terragrunt.hcl         ← sources terraform/logical
```

**Partitions:** `standard` (commercial AWS) and `gov` (AWS GovCloud).

**Environments** are presets of the feature flags below:

| Env                 | Purpose                                                        |
|---------------------|---------------------------------------------------------------|
| `min`               | Minimal single-AZ stack — dev / smoke testing                 |
| `full`              | Production preset — multi-AZ, DR, webhooks, BI all on          |
| `bi`                | Adds the business-intelligence database path                  |
| `hooks`             | Adds the webhooks (Kafka) path                                 |
| `workstation_setup` | Bootstrap/workstation tooling                                 |

New partitions and environments are scaffolded from [`live/.skel/`](./live/.skel)
via [`live/generate_live_env.sh`](./live/generate_live_env.sh).

For the full step-by-step deploy and upgrade procedure, see **[DEPLOYMENT.md](./DEPLOYMENT.md)**.

## Toolchain: the OpenTofu / Terraform split

The two layers deliberately run on different binaries:

- **`physical` → OpenTofu (`>= 1.11.1`).** It relies on write-only attributes
  (e.g. `master_password_wo`) and the Aurora module, which require a newer core than
  the logical layer is pinned to. Drive Terragrunt against it with
  `TERRAGRUNT_TFPATH=tofu`.
- **`logical` → Terraform (`1.5.7`).** Held at 1.5.7 for compatibility with the
  production runner (Spacelift, MPL-licensed Terraform). Uses the Helm / Kubernetes
  providers.

CI enforces this per-layer (see [`.github/workflows/terraform.yml`](./.github/workflows/terraform.yml)),
and the test harness selects the right binary per layer automatically.

> **Production deployments** are executed by **Spacelift**, configured in the
> separate `infra-live` repository (which wires each stack to the correct tool and
> version). This repo holds the modules and the `live/` definitions; it is not the
> Spacelift control plane.

## Configuration

Feature flags are set per-environment in `env.hcl` and flow through the root
`terragrunt.hcl` `inputs` merge. The most common operator-facing toggles:

| Variable                       | Layer    | Default | Purpose                                                        |
|--------------------------------|----------|---------|----------------------------------------------------------------|
| `customer`                     | physical | —       | Customer/stack name; drives resource naming and the subdomain  |
| `protect_resources`            | both     | `true`  | Deletion protection on RDS, S3, Vault secrets                   |
| `enable_dr`                    | physical | `true`  | Cross-region DR (RDS backup replication, S3 CRR)               |
| `db_engine`                    | physical | `rds`   | `rds` (provisioned MySQL) or `aurora` (Aurora Serverless v2)    |
| `rds_multi_az`                 | physical | `true`  | Multi-AZ RDS standby                                            |
| `highly_available_nat_gateway` | physical | `true`  | One NAT gateway per AZ                                          |
| `enable_webhooks`              | both     | `false` | Provision MSK (Kafka) for webhooks                             |
| `enable_bi`                    | both     | `false` | Provision the business-intelligence database path             |
| `image_tag` / `nextjs_tag`     | logical  | —       | Dozuki application image tags                                   |
| `chart_version`                | logical  | —       | Dozuki Helm chart version (pulled from the OCI/ECR registry)   |
| `image_repository`             | logical  | —       | Registry the app chart and images are pulled from              |

## Testing

[`live/tests/`](./live/tests) contains a Go/Terratest **upgrade harness**: it stands
up a stack at a baseline release, validates it, upgrades to a target release,
re-validates, and tears everything down — exercising the real upgrade path customers
take. Scenarios are defined in `matrix.yaml` (`min_default`, `bi_ha`, `full`).

- [`live/tests/verify-clean.sh`](./live/tests/verify-clean.sh) — read-only leak
  detector that scans the cost-heavy / collision-prone services for harness residue;
  use it as a post-run check or a gate before re-running.
- The harness runs on demand against a dedicated test account — it stands up real
  cloud resources — with a CI integration gate landing alongside the harness itself.

## CI/CD

| Workflow              | Trigger                     | Does                                                       |
|-----------------------|-----------------------------|-----------------------------------------------------------|
| `terraform.yml`       | push / PR                   | `fmt` + `tflint` per layer (OpenTofu for physical, Terraform for logical) |
| `release.yml`         | push of a `v*` tag          | Publishes the AWS GitHub release from the curated notes    |
| `release-azure.yml`   | Azure release tag           | Publishes the Azure deploy-kit release                     |

## Releasing

Releases are cut by tagging `vX.Y` (which fires `release.yml`). The working model is
freeze a release branch, land any further fixes on `master`, re-sync them into the
release branch, re-validate with the harness, then tag.

## Contributing

Install the [pre-commit](https://pre-commit.com/) hooks (`terraform fmt`,
`terraform-docs`, `tflint`) before committing:

```console
pre-commit install
pre-commit run -a       # run against all files
```

Make changes in `terraform/` (modules) and exercise them through a `min` environment
under `live/` per [DEPLOYMENT.md](./DEPLOYMENT.md). Keep the `physical` layer
OpenTofu-compatible and the `logical` layer Terraform 1.5.7-compatible.
