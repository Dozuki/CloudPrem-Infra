# Changelog

This changelog is maintained automatically by [release-please](https://github.com/googleapis/release-please) from Conventional Commit messages. Entries below 7.0.0 are not tracked here (see the GitHub Releases / git tags).

## [7.4.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.10...v7.4.0) (2026-07-03)


### Features

* **logical:** opt-in shared-grafana dashboards wiring, drop dead grafana sets ([#238](https://github.com/Dozuki/CloudPrem-Infra/issues/238)) ([302d8de](https://github.com/Dozuki/CloudPrem-Infra/commit/302d8dece6563e329b89c966159291f9d7860472))

## [7.3.10](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.9...v7.3.10) (2026-06-27)


### Bug Fixes

* **physical:** allow major version upgrades on the aurora cluster ([682503b](https://github.com/Dozuki/CloudPrem-Infra/commit/682503b56b6eeca03ef7bffbc8b08ff676ee242f))

## [7.3.9](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.8...v7.3.9) (2026-06-27)


### Bug Fixes

* **logical:** bump default chart_version 0.5.1 -&gt; 0.5.2 (per-IP rate limit 500 -&gt; 5000) ([d54a7be](https://github.com/Dozuki/CloudPrem-Infra/commit/d54a7be0a640e37c92e27480cb38c09abc82d616))

## [7.3.8](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.7...v7.3.8) (2026-06-27)


### Bug Fixes

* **logical:** bump default chart_version 0.5.0 -&gt; 0.5.1 (gzip-only compression + gateway perf fixes) ([7ec3a23](https://github.com/Dozuki/CloudPrem-Infra/commit/7ec3a2376b23fb1616c4e5be3bce27d96b492d44))

## [7.3.7](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.6...v7.3.7) (2026-06-27)


### Bug Fixes

* **logical:** EG CRD rate-limit requests int32 -&gt; int64 (K8s 1.34 rejects uint32-max on int32) ([5ba676e](https://github.com/Dozuki/CloudPrem-Infra/commit/5ba676efd865ab6c9259f2cbb9b1564aa022beb9))

## [7.3.6](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.5...v7.3.6) (2026-06-27)


### Bug Fixes

* **logical:** bump default chart_version 0.4.1 -&gt; 0.5.0 (gateway compression + proxy autoscaling) ([c608129](https://github.com/Dozuki/CloudPrem-Infra/commit/c6081295a83a6f2e9545eb9c28915f73c3eb76ec))
* **physical:** derive aurora parameter-group family from engine version ([1b60c12](https://github.com/Dozuki/CloudPrem-Infra/commit/1b60c12b1a89baa3f6ef88c31cc97ae6273a8de3))

## [7.3.5](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.4...v7.3.5) (2026-06-25)


### Bug Fixes

* **logical:** disable PROXY protocol on the Azure gateway (clientIP.mode=none) ([#231](https://github.com/Dozuki/CloudPrem-Infra/issues/231)) ([beabb3c](https://github.com/Dozuki/CloudPrem-Infra/commit/beabb3cb508cbe6a53b72048cb39bac2267d76fe))

## [7.3.4](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.3...v7.3.4) (2026-06-25)


### Bug Fixes

* **logical:** replace=true on helm_release.app so failed installs don't wedge retries ([#228](https://github.com/Dozuki/CloudPrem-Infra/issues/228)) ([69abd59](https://github.com/Dozuki/CloudPrem-Infra/commit/69abd59a52f706d3019b4bd60c99019f6cb8ba2f))

## [7.3.3](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.2...v7.3.3) (2026-06-25)


### Bug Fixes

* **physical:** dr_aurora subnet for_each empty case must be a set, not a tuple ([#226](https://github.com/Dozuki/CloudPrem-Infra/issues/226)) ([34016de](https://github.com/Dozuki/CloudPrem-Infra/commit/34016de6da690e64ecf27630dc9595c1f605280e))

## [7.3.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.1...v7.3.2) (2026-06-25)


### Bug Fixes

* **logical:** apply dozuki-operator image redirect on all clouds, not just Azure ([#224](https://github.com/Dozuki/CloudPrem-Infra/issues/224)) ([cbab2f6](https://github.com/Dozuki/CloudPrem-Infra/commit/cbab2f6fe1feb40f35828b6fe61afd0dce3188b4))

## [7.3.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.3.0...v7.3.1) (2026-06-24)


### Bug Fixes

* **azure:** helm provider authenticates to GHCR for the OCI chart pull ([#221](https://github.com/Dozuki/CloudPrem-Infra/issues/221)) ([9b0d3da](https://github.com/Dozuki/CloudPrem-Infra/commit/9b0d3da39dea838ba7b69ab598edff7fcd01daaf))

## [7.3.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.2.0...v7.3.0) (2026-06-24)


### Features

* delete_after tag + harness stamping for ResourceReaper ([#215](https://github.com/Dozuki/CloudPrem-Infra/issues/215)) ([0a7dc49](https://github.com/Dozuki/CloudPrem-Infra/commit/0a7dc496a76c9657d9c35d7063aba9af44936f26))


### Bug Fixes

* **azure:** public Key Vault when no CIDR allowlist + workloadidentity kubelogin ([#220](https://github.com/Dozuki/CloudPrem-Infra/issues/220)) ([9ab0477](https://github.com/Dozuki/CloudPrem-Infra/commit/9ab0477d285744fb7aef3931137e40f4779d8530))
* **live:** keep backend bools as bools (s3 encrypt regression) ([#218](https://github.com/Dozuki/CloudPrem-Infra/issues/218)) ([1c5fe9e](https://github.com/Dozuki/CloudPrem-Infra/commit/1c5fe9e0d1106fa0ac01c2cfd06689dd3bdc1faa))
* **logical:** ignore webhook-injected annotations on ratelimit redis ([#219](https://github.com/Dozuki/CloudPrem-Infra/issues/219)) ([9bea432](https://github.com/Dozuki/CloudPrem-Infra/commit/9bea43297ccbbec46a16957b854a14c2314b8dd8))

## [7.2.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.1.2...v7.2.0) (2026-06-24)


### Features

* **physical:** Aurora Global Database cross-region DR (phase 2) ([#210](https://github.com/Dozuki/CloudPrem-Infra/issues/210)) ([2b3ae48](https://github.com/Dozuki/CloudPrem-Infra/commit/2b3ae4849ef88d6f2dadd1210bfdaf7c8617c9ab))

## [7.1.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.1.1...v7.1.2) (2026-06-23)


### Bug Fixes

* sign Vault AWS-auth for the gov regional STS endpoint (GovCloud) ([#206](https://github.com/Dozuki/CloudPrem-Infra/issues/206)) ([a0684e8](https://github.com/Dozuki/CloudPrem-Infra/commit/a0684e8104c787ec5e13e27854193c8adb27b49a))

## [7.1.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.1.0...v7.1.1) (2026-06-23)


### Bug Fixes

* omit service_region for same-region Vault VPC endpoint (GovCloud) ([#204](https://github.com/Dozuki/CloudPrem-Infra/issues/204)) ([e3c8bde](https://github.com/Dozuki/CloudPrem-Infra/commit/e3c8bde5d68b16f7ee8b934ddedccf78b6acea0f))

## [7.1.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.0.0...v7.1.0) (2026-06-23)


### Features

* **logical:** manage Envoy Gateway CRDs via kubectl provider (fix EG upgrade timeout) ([#201](https://github.com/Dozuki/CloudPrem-Infra/issues/201)) ([085ab33](https://github.com/Dozuki/CloudPrem-Infra/commit/085ab3331907a964cfffee3a0c2a2c9a7c5bf899))
* **physical:** default RDS to a customer-managed KMS key (DR-ready posture) ([#199](https://github.com/Dozuki/CloudPrem-Infra/issues/199)) ([a70605c](https://github.com/Dozuki/CloudPrem-Infra/commit/a70605ce503a90fbcd90de84c7b3ad9974e5a8ea))


### Bug Fixes

* **physical:** stop Aurora module creating a 2nd SG in the default VPC ([#202](https://github.com/Dozuki/CloudPrem-Infra/issues/202)) ([24b6d13](https://github.com/Dozuki/CloudPrem-Infra/commit/24b6d13781fccaa9332ad34778bd8605adc9c919))

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
