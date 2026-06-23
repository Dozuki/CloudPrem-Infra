# Changelog

This changelog is maintained automatically by [release-please](https://github.com/googleapis/release-please) from Conventional Commit messages. Entries below 7.0.0 are not tracked here (see the GitHub Releases / git tags).

## [7.1.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.0.0...v7.1.0) (2026-06-23)


### Features

* default AWS to in-cluster memcached (destroy ElastiCache) ([#166](https://github.com/Dozuki/CloudPrem-Infra/issues/166)) ([58668eb](https://github.com/Dozuki/CloudPrem-Infra/commit/58668eb6c55466915c6fac64ccc24fae6826cecd))
* **eks:** add managed metrics-server addon for Auto Mode ([#186](https://github.com/Dozuki/CloudPrem-Infra/issues/186)) ([#187](https://github.com/Dozuki/CloudPrem-Infra/issues/187)) ([8478f4f](https://github.com/Dozuki/CloudPrem-Infra/commit/8478f4fe04df5d251e1cf99e039c6332dbfb759a))
* enable EKS control-plane audit logging on customer env clusters ([#132](https://github.com/Dozuki/CloudPrem-Infra/issues/132)) ([ebe5213](https://github.com/Dozuki/CloudPrem-Infra/commit/ebe52137222f7bb3f9d2dac944e16dba46bb81d4))
* **harness:** final run-summary banner (result, refs, phases+durations, artifacts) ([#183](https://github.com/Dozuki/CloudPrem-Infra/issues/183)) ([0e41ba0](https://github.com/Dozuki/CloudPrem-Infra/commit/0e41ba067c730da3e539e5ab2aac33d85b6c1178))
* **loadtest:** standalone k6 load-test harness ([#171](https://github.com/Dozuki/CloudPrem-Infra/issues/171)) ([9f7028a](https://github.com/Dozuki/CloudPrem-Infra/commit/9f7028a268870015e6e74fb6d8d350657d65129a))
* **logical:** azure auto-DNS (external-dns) + Let's Encrypt TLS ([#158](https://github.com/Dozuki/CloudPrem-Infra/issues/158)) ([a4a0dac](https://github.com/Dozuki/CloudPrem-Infra/commit/a4a0dac74e86b136f6dfd416fba8379ef694d86b))
* **logical:** consume dozuki chart from ecr, remove helm submodule ([#145](https://github.com/Dozuki/CloudPrem-Infra/issues/145)) ([da87ed0](https://github.com/Dozuki/CloudPrem-Infra/commit/da87ed0df869d174577988c8b3b1c14ecb431183))
* **logical:** customer TLS via Vault ESO (forward-port from mcp-deploy-integration) ([#194](https://github.com/Dozuki/CloudPrem-Infra/issues/194)) ([6274c5a](https://github.com/Dozuki/CloudPrem-Infra/commit/6274c5a55cd60d1bf67c115d7b570c78b2c1dc1f))
* **logical:** expose gateway via azure LoadBalancer with DNS label ([#157](https://github.com/Dozuki/CloudPrem-Infra/issues/157)) ([6112b08](https://github.com/Dozuki/CloudPrem-Infra/commit/6112b086d15fe3f5f852d841f19eb91531923ebd))
* **logical:** fix azure-port bugs (DB CA, gateway TLS, operator image) ([#153](https://github.com/Dozuki/CloudPrem-Infra/issues/153)) ([421f90e](https://github.com/Dozuki/CloudPrem-Infra/commit/421f90e4c1e67eb524094f2f6edb9a3a28fc3d92))
* **logical:** serve azure objects from a public S3 host (image display) ([#163](https://github.com/Dozuki/CloudPrem-Infra/issues/163)) ([e33f9fb](https://github.com/Dozuki/CloudPrem-Infra/commit/e33f9fb3ea564ce327140368071884f7c9b0325c))
* **physical:** add S3 Gateway VPC endpoint for created VPCs ([#168](https://github.com/Dozuki/CloudPrem-Infra/issues/168)) ([b46f5f7](https://github.com/Dozuki/CloudPrem-Infra/commit/b46f5f71de18d1bf197ab7eea3971d3305737a79))
* **physical:** Aurora Serverless v2 path behind db_engine toggle (Plan A) ([#149](https://github.com/Dozuki/CloudPrem-Infra/issues/149)) ([1ee9d5a](https://github.com/Dozuki/CloudPrem-Infra/commit/1ee9d5a0fc5d1ee70a48ffed688af172f700d905))
* **physical:** default db_engine=aurora (was rds) ([174a2ed](https://github.com/Dozuki/CloudPrem-Infra/commit/174a2ede3e872941d5af8c81e22605b7a7b38082))


### Bug Fixes

* **azure:** enforce TLS on Azure MySQL (require_secure_transport ON) ([#191](https://github.com/Dozuki/CloudPrem-Infra/issues/191)) ([b1d31d0](https://github.com/Dozuki/CloudPrem-Infra/commit/b1d31d0e839b7b592ffbd585354d97f24d4cde8c))
* **ci:** repair infracost config ([#147](https://github.com/Dozuki/CloudPrem-Infra/issues/147)) ([0ee1854](https://github.com/Dozuki/CloudPrem-Infra/commit/0ee1854e3c8316b89067d5ae0ab678f4114c49ce))
* **eks:** remove leftover physical cloudwatch-observability addon (lives in logical) ([7ebac89](https://github.com/Dozuki/CloudPrem-Infra/commit/7ebac8947940365b8fab10dcbc846d4430aed8ce))
* **harness:** bundle per-config diagnostics dirs, not just the run-log dir ([#182](https://github.com/Dozuki/CloudPrem-Infra/issues/182)) ([20d7f78](https://github.com/Dozuki/CloudPrem-Infra/commit/20d7f78040517145297c8f4ab547d0d930150a42))
* **harness:** cleanup-orphans terragrunt destroy needs -auto-approve (EOF in non-tty trap) ([#167](https://github.com/Dozuki/CloudPrem-Infra/issues/167)) ([828efde](https://github.com/Dozuki/CloudPrem-Infra/commit/828efdea9eff0a2a8b87b4737d7eb3e0bf6d01d1))
* **harness:** clear NLB deletion-protection before physical destroy ([#176](https://github.com/Dozuki/CloudPrem-Infra/issues/176)) ([5e34b40](https://github.com/Dozuki/CloudPrem-Infra/commit/5e34b409a9693f47ec5aca0b335fddc3d72ccf22))
* **harness:** re-authenticate to Vault in cleanup (don't trust stale inherited token) ([#174](https://github.com/Dozuki/CloudPrem-Infra/issues/174)) ([3813cb3](https://github.com/Dozuki/CloudPrem-Infra/commit/3813cb3bb8436072fd09639578f736b1ba2f6f6e))
* **logical:** azure deploy fixes — external-dns flags/txtPrefix + S3 region ([#161](https://github.com/Dozuki/CloudPrem-Infra/issues/161)) ([ce71b35](https://github.com/Dozuki/CloudPrem-Infra/commit/ce71b35a0356091ae26742f81f444c9cb521ab2d))
* **logical:** azurerm provider must not need Azure CLI on AWS deploys ([a0224cf](https://github.com/Dozuki/CloudPrem-Infra/commit/a0224cfe2677e4493f640496a553fc08f883d052))
* **logical:** cloud-agnostic manual TLS (external cert/key on AWS too) ([#169](https://github.com/Dozuki/CloudPrem-Infra/issues/169)) ([e894541](https://github.com/Dozuki/CloudPrem-Infra/commit/e89454158882334f05778e77546d856dfc05943d))
* **logical:** configure azurerm via provider for_each (zero instances on AWS) ([be23fc1](https://github.com/Dozuki/CloudPrem-Infra/commit/be23fc132c46881fe42ee93567225f5e7fad3e77))
* **logical:** drop adopt null_resource for cloudwatch addon (broke upgrades) ([21d8ace](https://github.com/Dozuki/CloudPrem-Infra/commit/21d8acec67dc32ceb93b29d3f5dee1d47e9840d1))
* **logical:** in-cluster memcached host must be the service FQDN (not bare name) ([#180](https://github.com/Dozuki/CloudPrem-Infra/issues/180)) ([71b8bee](https://github.com/Dozuki/CloudPrem-Infra/commit/71b8bee1f56f73d634f7246429b5e603cc731f54))
* **logical:** patch coredns-custom data instead of creating it (AKS pre-creates it) ([#165](https://github.com/Dozuki/CloudPrem-Infra/issues/165)) ([ed0edfd](https://github.com/Dozuki/CloudPrem-Infra/commit/ed0edfd8313a2701db057e5e6a34742674db259b))
* **logical:** render SUPPLIED manual-TLS via the chart, not Terraform (fix v6.0-&gt;v6.1 upgrade) ([#178](https://github.com/Dozuki/CloudPrem-Infra/issues/178)) ([ceae06a](https://github.com/Dozuki/CloudPrem-Infra/commit/ceae06a3b4df587621c4aaba5ef4899dcdda9eec))
* **logical:** seed memcached host as FQDN in ALL paths incl. the Vault cache secret ([#181](https://github.com/Dozuki/CloudPrem-Infra/issues/181)) ([1191fbd](https://github.com/Dozuki/CloudPrem-Infra/commit/1191fbdc7ed475d70bc5cfc49f0736a23b72de40))
* **logical:** set ghcr-pull imagePullSecret on dozuki-operator for azure ([#155](https://github.com/Dozuki/CloudPrem-Infra/issues/155)) ([44d22e8](https://github.com/Dozuki/CloudPrem-Infra/commit/44d22e85b980c627a8811ddded625157d1163aa1))
* **physical:** aurora_engine_version full RDS format (8.4.mysql_aurora.8.4.7) ([3d46ba7](https://github.com/Dozuki/CloudPrem-Infra/commit/3d46ba7c581497c0d27fae306dad00f56659e1ab))
* **physical:** auto-adopt pre-existing amazon-cloudwatch-observability addon ([#156](https://github.com/Dozuki/CloudPrem-Infra/issues/156)) ([26ac02f](https://github.com/Dozuki/CloudPrem-Infra/commit/26ac02f87683c26ad2c2b636e04654481ad90b1d))
* **physical:** clean teardown — NLB deletion protection + aws.dr provider ([#151](https://github.com/Dozuki/CloudPrem-Infra/issues/151)) ([8b3cedd](https://github.com/Dozuki/CloudPrem-Infra/commit/8b3cedd4cbfc9d4eead1e06c8f6421e21f51bd41))
* **physical:** gate KMS deletion_window_in_days on protect_resources (30 prod / 7 dev) ([#173](https://github.com/Dozuki/CloudPrem-Infra/issues/173)) ([00b0629](https://github.com/Dozuki/CloudPrem-Infra/commit/00b0629aecc1832910e0c479b649e577ae98bf91))
* **physical:** give cloudwatch-observability addon a 40m create/update timeout ([06f1bbb](https://github.com/Dozuki/CloudPrem-Infra/commit/06f1bbbf9430eeb188e18ccd4729c2933db5484a))
* set AWS_REGION on s3_replication_job_init local-exec ([#133](https://github.com/Dozuki/CloudPrem-Infra/issues/133)) ([cd969d7](https://github.com/Dozuki/CloudPrem-Infra/commit/cd969d7ae39403cb77577c4b986925f81018753e))
* use data.aws_region.current.region (.id deprecated) — physical + logical ([#192](https://github.com/Dozuki/CloudPrem-Infra/issues/192)) ([8a5c429](https://github.com/Dozuki/CloudPrem-Infra/commit/8a5c429f94e7822ffb5bf05c3c7e15a2e420ca1a))

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
