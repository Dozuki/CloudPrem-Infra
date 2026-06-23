# Changelog

This changelog is maintained automatically by [release-please](https://github.com/googleapis/release-please) from Conventional Commit messages. Entries below 7.0.0 are not tracked here (see the GitHub Releases / git tags).

## 7.0.0

The biggest release since the v6.0 EKS Auto Mode rearchitecture — adds a second cloud (Azure) and changes several defaults. Read the migration notes before upgrading.

### ⚠️ Breaking changes / migration notes

* Helm chart is now consumed from ECR as an OCI artifact (#145) — no longer a git submodule; the logical layer pulls `oci://<image_repository>/charts/dozuki` at `chart_version` (default `0.4.1`). The pinned version must be published to your ECR before applying.
* Physical layer now requires OpenTofu 1.12.x (#149) — the Aurora module's `required_version` is evaluated at init even for `db_engine="rds"`. All physical stacks must run OpenTofu 1.12.x; the logical layer also runs OpenTofu now (azurerm provider `for_each`).
* Aurora is now the default `db_engine` (#149) — new stacks come up on Aurora MySQL 8.4 Serverless v2. Existing `rds` stacks must pin `db_engine="rds"` or the infra-live `db-replace-guard` Spacelift policy will block the DB replacement.
* In-cluster memcached is now the default on AWS (#166) — `memcached_in_cluster` defaults to `true`; ElastiCache is no longer provisioned and is **destroyed if it exists**. Set `memcached_in_cluster=false` to keep ElastiCache.

### ✨ Features

* DR Phase 1: cross-region backup/restore data layer (#138)
* EKS control-plane audit logging (#132)
* Aurora Serverless v2, now the default (#149)
* Azure support — multi-cloud foundation (#140, #152) and deploy enhancements (#157, #158, #161, #163, #169)
* Gateway rate limiting + Envoy Gateway 1.8.1 (#184)
* Customer-provided TLS via Vault (#194)
* Managed metrics-server addon for EKS Auto Mode (#186)
* S3 Gateway VPC endpoint for created VPCs (#168)

### 🐛 Fixes

* CloudWatch alarms stuck in INSUFFICIENT_DATA (#137); cloudwatch-observability addon moved to the logical layer
* `AWS_REGION` on S3 replication job init (#133)
* `data.aws_region.current.id` → `.region` (#192)
* azurerm provider no longer needs the Azure CLI on AWS deploys
* NLB deletion protection blocked teardown (#151); missing `aws.dr` provider in the live root (#151)
* Deterministic lambda packaging + DMS-start wait (#193)
* Supplied manual-TLS rendered by the chart, fixing the v6.0→v7.0 upgrade collision (#178)
* KMS key deletion window gated on `protect_resources` (#173)
* In-cluster memcached host seeded as the service FQDN (#180, #181)
* TLS enforced on Azure MySQL (#191)
