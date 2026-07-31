# Changelog

This changelog is maintained automatically by [release-please](https://github.com/googleapis/release-please) from Conventional Commit messages. Entries below 7.0.0 are not tracked here (see the GitHub Releases / git tags).

## [8.6.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.5.1...v8.6.0) (2026-07-31)


### Features

* **physical:** aurora migration runner recovery phases ([#394](https://github.com/Dozuki/CloudPrem-Infra/issues/394)) ([8395b72](https://github.com/Dozuki/CloudPrem-Infra/commit/8395b72091a231a27ad3374066aec4ea1a6af664))


### Bug Fixes

* **physical:** BI DMS on default aws/dms keys, serverless cannot use customer keys ([#397](https://github.com/Dozuki/CloudPrem-Infra/issues/397)) ([da358d7](https://github.com/Dozuki/CloudPrem-Infra/commit/da358d7842a06e192227f5c442637c0b27ceddf7))

## [8.5.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.5.0...v8.5.1) (2026-07-31)


### Bug Fixes

* **physical:** grant the DMS service principal on the BI key for serverless compute ([#392](https://github.com/Dozuki/CloudPrem-Infra/issues/392)) ([085a7e6](https://github.com/Dozuki/CloudPrem-Infra/commit/085a7e6d786c60a9673324ff02638733c1228897))

## [8.5.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.4.0...v8.5.0) (2026-07-31)


### Features

* **logical:** fail fast on missing artifacts, retry and self-heal flux drift ([b37912c](https://github.com/Dozuki/CloudPrem-Infra/commit/b37912ca9742c77c844b3ce7e8f44ebef2848532))
* **logical:** tag dynamic EBS volumes with deleteAfter on ephemeral deploys ([#391](https://github.com/Dozuki/CloudPrem-Infra/issues/391)) ([6796515](https://github.com/Dozuki/CloudPrem-Infra/commit/67965150b8fa160672e75f9c19734458d2d31550))


### Bug Fixes

* **monitoring:** skip the INSUFFICIENT_DATA to OK transition in slack alerts ([#390](https://github.com/Dozuki/CloudPrem-Infra/issues/390)) ([4584db7](https://github.com/Dozuki/CloudPrem-Infra/commit/4584db7d0177ed7782f980a07e365dbf66b9f615))

## [8.4.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.3.4...v8.4.0) (2026-07-30)


### Features

* **bi:** BI database on Aurora Serverless v2, and default the RDS engine to 8.4 ([#376](https://github.com/Dozuki/CloudPrem-Infra/issues/376)) ([c9186cf](https://github.com/Dozuki/CloudPrem-Infra/commit/c9186cf025c9d96af593a6693e1bcc90d88691fd))

## [8.3.4](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.3.3...v8.3.4) (2026-07-30)


### Bug Fixes

* **physical:** app pods could never delete guide PDFs, grant s3:DeleteObject ([2a7b5a7](https://github.com/Dozuki/CloudPrem-Infra/commit/2a7b5a732435d8346a85435b0a824e6879ca16cc))

## [8.3.3](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.3.2...v8.3.3) (2026-07-30)


### Bug Fixes

* **dr:** stop the rds snapshot guard denying aurora a CMK ([#384](https://github.com/Dozuki/CloudPrem-Infra/issues/384)) ([8a3019c](https://github.com/Dozuki/CloudPrem-Infra/commit/8a3019cd5416668c32d0cbe954ef6744773a1530))
* **eks:** tag the iam policies and pod identity associations ([#382](https://github.com/Dozuki/CloudPrem-Infra/issues/382)) ([92188f8](https://github.com/Dozuki/CloudPrem-Infra/commit/92188f8cd27cfc4b420617ed983f8f9e761c747e))

## [8.3.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.3.1...v8.3.2) (2026-07-30)


### Bug Fixes

* **dr:** stop telling operators aurora DR needs a CMK ([#381](https://github.com/Dozuki/CloudPrem-Infra/issues/381)) ([87249d7](https://github.com/Dozuki/CloudPrem-Infra/commit/87249d794b7ec59fc5ddcb06100f5f0ac6f13690))
* **physical:** rotate the S3 key and tag the resources that were missing tags ([#379](https://github.com/Dozuki/CloudPrem-Infra/issues/379)) ([e103782](https://github.com/Dozuki/CloudPrem-Infra/commit/e10378201fbcfe2bc534157c1d5cc0d8d976fd88))

## [8.3.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.3.0...v8.3.1) (2026-07-30)


### Bug Fixes

* **logical:** reuse global ops basic auth ([#377](https://github.com/Dozuki/CloudPrem-Infra/issues/377)) ([fd2beca](https://github.com/Dozuki/CloudPrem-Infra/commit/fd2beca72c73d97f53e1a9b039cc6c0c88c47358))

## [8.3.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.2.0...v8.3.0) (2026-07-30)


### Features

* **logical:** wire the memcached proxy tier through, default off ([#373](https://github.com/Dozuki/CloudPrem-Infra/issues/373)) ([3038799](https://github.com/Dozuki/CloudPrem-Infra/commit/30387997e942c97aee5b0630405892600d63f9ca))

## [8.2.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.1.2...v8.2.0) (2026-07-30)


### Features

* **logical:** enable Alertmanager Slack notifications ([#370](https://github.com/Dozuki/CloudPrem-Infra/issues/370)) ([b1f12b7](https://github.com/Dozuki/CloudPrem-Infra/commit/b1f12b7049269fa96344b08b0a9527b0603d5448))
* **physical:** make a customer-managed database key the default ([#371](https://github.com/Dozuki/CloudPrem-Infra/issues/371)) ([2b46ee6](https://github.com/Dozuki/CloudPrem-Infra/commit/2b46ee694dc67432da2f796d805a698febdd4d96))

## [8.1.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.1.1...v8.1.2) (2026-07-29)


### Bug Fixes

* **deps:** commit provider lock files and adopt releases on a soak delay ([#364](https://github.com/Dozuki/CloudPrem-Infra/issues/364)) ([910f4b1](https://github.com/Dozuki/CloudPrem-Infra/commit/910f4b13342f1428a56ac55c445f8346db7ba7ce))
* **deps:** move the 6.57.0 exclusion into renovate, out of required_providers ([1fd2b71](https://github.com/Dozuki/CloudPrem-Infra/commit/1fd2b7133deeb5f5570e94606410d969edc37c9a))
* **live:** migrate skip and retryable_errors to the exclude and errors blocks ([3ae853e](https://github.com/Dozuki/CloudPrem-Infra/commit/3ae853ecc90be9ad6fde99217664f106d5ed157b))
* **logical:** stop karpenter evicting helm-controller mid-upgrade ([#368](https://github.com/Dozuki/CloudPrem-Infra/issues/368)) ([1062ffc](https://github.com/Dozuki/CloudPrem-Infra/commit/1062ffc0e44af94367bce073820ace4f2d15c6f2))

## [8.1.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.1.0...v8.1.1) (2026-07-29)


### Bug Fixes

* **physical:** exclude aws provider 6.57.0, it breaks every data-source read ([6f6c969](https://github.com/Dozuki/CloudPrem-Infra/commit/6f6c969f807b2c5f031ff772de68ac20141e31bd))

## [8.1.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.0.4...v8.1.0) (2026-07-29)


### Features

* **physical:** DR dead-man detection and acknowledged paging ([#358](https://github.com/Dozuki/CloudPrem-Infra/issues/358)) ([153922c](https://github.com/Dozuki/CloudPrem-Infra/commit/153922c0c5154717555e73df0aa045e519fd666b))


### Bug Fixes

* **loadtest:** authenticate page GETs with the real session cookie ([#362](https://github.com/Dozuki/CloudPrem-Infra/issues/362)) ([b678060](https://github.com/Dozuki/CloudPrem-Infra/commit/b6780604535ae0da6ac7d9ee6246f41dbb49eff6))
* **physical:** derive the secret replica region from the aws.dr provider ([#359](https://github.com/Dozuki/CloudPrem-Infra/issues/359)) ([126240c](https://github.com/Dozuki/CloudPrem-Infra/commit/126240ca596d287ebcb54bee2297ad04207a1407))
* **physical:** dms restart lambda must never touch the aurora migration task ([50e13a2](https://github.com/Dozuki/CloudPrem-Infra/commit/50e13a216ffb06061cca651c6c9a328e817c1bd1))
* **physical:** export AWS_PROFILE in create-s3-batch instead of using a command prefix ([01ec926](https://github.com/Dozuki/CloudPrem-Infra/commit/01ec9265e59361b471ecbbb0d6956f7a3f1b6e6f))
* **physical:** wave-2 followups - re-enterable fence, xlarge migration instance default ([#361](https://github.com/Dozuki/CloudPrem-Infra/issues/361)) ([68ac9a7](https://github.com/Dozuki/CloudPrem-Infra/commit/68ac9a788c3761283865bdece07e81b820036c43))

## [8.0.4](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.0.3...v8.0.4) (2026-07-28)


### Bug Fixes

* **physical:** log exports and snapshot tags on the DR aurora secondary ([#356](https://github.com/Dozuki/CloudPrem-Infra/issues/356)) ([492d475](https://github.com/Dozuki/CloudPrem-Infra/commit/492d475cacf990c5dcb548420cb95ef1942080b5))

## [8.0.3](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.0.2...v8.0.3) (2026-07-28)


### Bug Fixes

* **physical:** give the DR secondary a Serverless v2 scaling config ([#352](https://github.com/Dozuki/CloudPrem-Infra/issues/352)) ([d796e95](https://github.com/Dozuki/CloudPrem-Infra/commit/d796e957d28b9bbf325183541ba142a93c399e19))
* **physical:** intelligent-tiering on the primary content buckets ([#353](https://github.com/Dozuki/CloudPrem-Infra/issues/353)) ([a4175cc](https://github.com/Dozuki/CloudPrem-Infra/commit/a4175cc98bb371a9d029bb7e1bfc0501bec243cf))
* **physical:** replicate the DB credentials secret to the DR region ([#355](https://github.com/Dozuki/CloudPrem-Infra/issues/355)) ([b7bb30b](https://github.com/Dozuki/CloudPrem-Infra/commit/b7bb30b5012c93cb1388b725427c908fd36c5296))

## [8.0.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.0.1...v8.0.2) (2026-07-28)


### Bug Fixes

* **physical:** aurora fence drain-proof must stop the task before asserting the checkpoint ([#349](https://github.com/Dozuki/CloudPrem-Infra/issues/349)) ([710cc9d](https://github.com/Dozuki/CloudPrem-Infra/commit/710cc9d0417310a683ccc37c2e2ced72df2678ae))
* **physical:** dr bucket lifecycle + ssl-only policies on content buckets ([#347](https://github.com/Dozuki/CloudPrem-Infra/issues/347)) ([1fd3973](https://github.com/Dozuki/CloudPrem-Infra/commit/1fd397339620162b67cc9602f4b1bf921aab5aeb))
* **physical:** restore acl support on the pdf bucket ([#348](https://github.com/Dozuki/CloudPrem-Infra/issues/348)) ([0a5eef7](https://github.com/Dozuki/CloudPrem-Infra/commit/0a5eef727aea72b18c6e3bd2bd5f4e5e7921b18a))

## [8.0.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v8.0.0...v8.0.1) (2026-07-28)


### Bug Fixes

* **logical:** keep EBS-backed workloads off spot and stop stranding them ([#345](https://github.com/Dozuki/CloudPrem-Infra/issues/345)) ([350ceec](https://github.com/Dozuki/CloudPrem-Infra/commit/350ceec81a9b0ff0a73dc18de4d34fe47b8cc9c2))

## [8.0.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.23.5...v8.0.0) (2026-07-28)


### ⚠ BREAKING CHANGES

* **webhooks:** enable_webhooks and msk_bootstrap_brokers are gone from both layers, along with the MSK cluster the flag provisioned. Any stack still setting enable_webhooks will fail with an unsupported-variable error and must drop the line.

### Features

* **logical:** datadog apm + continuous profiler behind enable_datadog ([#251](https://github.com/Dozuki/CloudPrem-Infra/issues/251)) ([5e7ec10](https://github.com/Dozuki/CloudPrem-Infra/commit/5e7ec1005e9921801829b342479a7a8d8ec5f255))
* **webhooks:** remove webhooks, frontegg and MSK from both layers ([#343](https://github.com/Dozuki/CloudPrem-Infra/issues/343)) ([f9ab850](https://github.com/Dozuki/CloudPrem-Infra/commit/f9ab850e7e27ae2b5ef42a509a45210d4ffa8245))

## [7.23.5](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.23.4...v7.23.5) (2026-07-28)


### Bug Fixes

* **physical:** dms slack alerts - regional task link, page only on failures ([#340](https://github.com/Dozuki/CloudPrem-Infra/issues/340)) ([952a4ba](https://github.com/Dozuki/CloudPrem-Infra/commit/952a4baac3873744ce3c27763a22e930be58b485))

## [7.23.4](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.23.3...v7.23.4) (2026-07-28)


### Bug Fixes

* **physical:** scope dms event alerts to own tasks + apply resizes immediately ([#338](https://github.com/Dozuki/CloudPrem-Infra/issues/338)) ([492424e](https://github.com/Dozuki/CloudPrem-Infra/commit/492424ef22873e112f8f6e21d2e110da16c04371))

## [7.23.3](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.23.2...v7.23.3) (2026-07-28)


### Bug Fixes

* **logical:** dedupe flux alert fields to env + versions ([#336](https://github.com/Dozuki/CloudPrem-Infra/issues/336)) ([d05c40a](https://github.com/Dozuki/CloudPrem-Infra/commit/d05c40aa9fa0e2d65224a4aafce6313cf0dd7ac6))

## [7.23.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.23.1...v7.23.2) (2026-07-28)


### Bug Fixes

* **logical:** ignore hpa-owned spec.replicas in flux drift detection ([#334](https://github.com/Dozuki/CloudPrem-Infra/issues/334)) ([709ebf0](https://github.com/Dozuki/CloudPrem-Infra/commit/709ebf09246b0ff7167b874740640b92bd38577d))

## [7.23.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.23.0...v7.23.1) (2026-07-28)


### Bug Fixes

* **logical:** stamp env identity on flux slack notifications ([#331](https://github.com/Dozuki/CloudPrem-Infra/issues/331)) ([627c566](https://github.com/Dozuki/CloudPrem-Infra/commit/627c5669e2b948bdb3e7c2b114696d8e276e0ab0))

## [7.23.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.22.1...v7.23.0) (2026-07-28)


### Features

* **logical:** flux drift-warn, slack alerts, crds CreateReplace, maxHistory ([#328](https://github.com/Dozuki/CloudPrem-Infra/issues/328)) ([3d1891a](https://github.com/Dozuki/CloudPrem-Infra/commit/3d1891aaae4f39d03a0ffdb2b5683e9f95f0e70a))


### Bug Fixes

* **logical:** key grafana-db-create job name on db_resource_id ([#329](https://github.com/Dozuki/CloudPrem-Infra/issues/329)) ([1d12964](https://github.com/Dozuki/CloudPrem-Infra/commit/1d129640bd35e0eaac2a85c9bf970d0ab3581683))
* **logical:** stop auto-instrumenting workloads that cannot use it ([9bcc7cd](https://github.com/Dozuki/CloudPrem-Infra/commit/9bcc7cd1ecc4068752fcd722c563e185c37bb3ee))
* **physical:** aurora runner bastion discovery + MySQL-8-safe object dump ([#326](https://github.com/Dozuki/CloudPrem-Infra/issues/326)) ([2983a2c](https://github.com/Dozuki/CloudPrem-Infra/commit/2983a2cd6e87dcfbcc287d263f76d5b323ed0124))
* **physical:** aurora runner load-phase DMS state sequencing ([#327](https://github.com/Dozuki/CloudPrem-Infra/issues/327)) ([e059107](https://github.com/Dozuki/CloudPrem-Infra/commit/e05910744253c984e6e8ddb967311557b70e7c05))
* **physical:** aurora runner secret lookup breaks under list-secrets pagination ([#324](https://github.com/Dozuki/CloudPrem-Infra/issues/324)) ([30a904d](https://github.com/Dozuki/CloudPrem-Infra/commit/30a904d3a24ca1f8b60e18db0c71f3bd5d19ab3e))
* **physical:** DR replica buckets were never destroyable ([c195c5b](https://github.com/Dozuki/CloudPrem-Infra/commit/c195c5b7754531f834d653d4f575052ea5d6c32c))
* **physical:** drop explicit lower_case_table_names from the aurora cluster PG ([#330](https://github.com/Dozuki/CloudPrem-Infra/issues/330)) ([579a3f6](https://github.com/Dozuki/CloudPrem-Infra/commit/579a3f674b8c82cf0e5e64022845fffe6394267c))

## [7.22.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.22.0...v7.22.1) (2026-07-27)


### Bug Fixes

* **physical:** aurora migration review round 2 - fence ownership, creds, gates ([#322](https://github.com/Dozuki/CloudPrem-Infra/issues/322)) ([6d9ab81](https://github.com/Dozuki/CloudPrem-Infra/commit/6d9ab81901726fa03c6af0731b301d9d31339300))

## [7.22.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.21.3...v7.22.0) (2026-07-27)


### Features

* **physical:** aurora_migration_state - RDS to Aurora alongside-migration rig ([#320](https://github.com/Dozuki/CloudPrem-Infra/issues/320)) ([9f0623c](https://github.com/Dozuki/CloudPrem-Infra/commit/9f0623c9b9b9b76f71b42acb3b705bf50e38c2a4))

## [7.21.3](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.21.2...v7.21.3) (2026-07-27)


### Bug Fixes

* **logical:** give event-service real frontegg credentials ([#316](https://github.com/Dozuki/CloudPrem-Infra/issues/316)) ([cddf57d](https://github.com/Dozuki/CloudPrem-Infra/commit/cddf57d490f66c9af52a27fdd6c168f74dc694e1))

## [7.21.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.21.1...v7.21.2) (2026-07-26)


### Bug Fixes

* **logical:** pin the connectivity images to the versions the chart actually uses ([#314](https://github.com/Dozuki/CloudPrem-Infra/issues/314)) ([5e74f09](https://github.com/Dozuki/CloudPrem-Infra/commit/5e74f09df34a90563b11c9eb2da7236b219094a5))

## [7.21.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.21.0...v7.21.1) (2026-07-26)


### Bug Fixes

* **logical:** carve metrics-server out of namespace-wide STRICT mTLS ([#312](https://github.com/Dozuki/CloudPrem-Infra/issues/312)) ([dfe4ba2](https://github.com/Dozuki/CloudPrem-Infra/commit/dfe4ba2843697e54c11d8ff85b531026ae7ecbc7))

## [7.21.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.20.0...v7.21.0) (2026-07-26)


### Features

* **logical:** values diff view ConfigMap for per-key plan diffs ([#309](https://github.com/Dozuki/CloudPrem-Infra/issues/309)) ([9bdd7c1](https://github.com/Dozuki/CloudPrem-Infra/commit/9bdd7c159828e1cdd1557b0213c8a91a9d9d473b))

## [7.20.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.19.4...v7.20.0) (2026-07-26)


### Features

* **logical:** pull the frontegg connectivity images from our own registry ([#307](https://github.com/Dozuki/CloudPrem-Infra/issues/307)) ([0e9c27e](https://github.com/Dozuki/CloudPrem-Infra/commit/0e9c27ed557bf412db6c6b55b0c7889d78770abf))

## [7.19.4](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.19.3...v7.19.4) (2026-07-26)


### Bug Fixes

* **physical:** abort incomplete multipart uploads on DR buckets ([e70f7ee](https://github.com/Dozuki/CloudPrem-Infra/commit/e70f7ee838594dbece30544b2684a4664a8b4b4a))

## [7.19.3](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.19.2...v7.19.3) (2026-07-26)


### Bug Fixes

* **logical:** grafana MySQL TLS needs a CA cert path (skip-verify still reads it) ([690f87c](https://github.com/Dozuki/CloudPrem-Infra/commit/690f87ca0fbc46509a5d0ac329bdc8d2eb1907e8))

## [7.19.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.19.1...v7.19.2) (2026-07-26)


### Bug Fixes

* **logical:** grafana backend MySQL needs TLS (RDS require_secure_transport) ([f7f8e7e](https://github.com/Dozuki/CloudPrem-Infra/commit/f7f8e7e513e60b04a8c03b99b2842459572e3832))

## [7.19.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.19.0...v7.19.1) (2026-07-26)


### Bug Fixes

* **physical:** wait for the primary before creating the Aurora DR replica ([#301](https://github.com/Dozuki/CloudPrem-Infra/issues/301)) ([a2ffdeb](https://github.com/Dozuki/CloudPrem-Infra/commit/a2ffdeb6b1993dbf69372b755c84d79ca6f387d4))

## [7.19.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.18.0...v7.19.0) (2026-07-26)


### Features

* **logical:** deliver the dozuki app via Flux (HelmRelease), not the TF helm provider ([#295](https://github.com/Dozuki/CloudPrem-Infra/issues/295)) ([d7099ff](https://github.com/Dozuki/CloudPrem-Infra/commit/d7099ff1361a18db9168a4725fa375e860bfb92f))
* **metrics-server:** retire the EKS addon, let the chart own it ([#297](https://github.com/Dozuki/CloudPrem-Infra/issues/297)) ([e433c4a](https://github.com/Dozuki/CloudPrem-Infra/commit/e433c4a74c5147e6cc389e9b25f4e07c549acb0a))


### Bug Fixes

* **logical:** DMS endpoint test-connection, and correct the connectivity secret key ([#294](https://github.com/Dozuki/CloudPrem-Infra/issues/294)) ([55b9a70](https://github.com/Dozuki/CloudPrem-Infra/commit/55b9a704cf55e0054e53111632a0dafa855750e4))

## [7.18.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.17.2...v7.18.0) (2026-07-26)


### Features

* **logical:** envoy gateway 1.8.3, and keep the CRDs out of the run log ([#289](https://github.com/Dozuki/CloudPrem-Infra/issues/289)) ([7bcd3d4](https://github.com/Dozuki/CloudPrem-Infra/commit/7bcd3d4745066e7f75085ed2f1e4081ec85ca914))


### Bug Fixes

* **logical:** exclude the frontegg services from otel auto-instrumentation ([#293](https://github.com/Dozuki/CloudPrem-Infra/issues/293)) ([d142284](https://github.com/Dozuki/CloudPrem-Infra/commit/d142284917f975942cb595e341c985efbc2ab8b1))
* **logical:** source connectivity DB passwords from the ESO secret ([#290](https://github.com/Dozuki/CloudPrem-Infra/issues/290)) ([4f972c1](https://github.com/Dozuki/CloudPrem-Infra/commit/4f972c10cbfbd22303c25e02da28460427713f88))
* **physical:** grant dms:DescribeReplicationTasks to the app pod identity ([#292](https://github.com/Dozuki/CloudPrem-Infra/issues/292)) ([e2499c3](https://github.com/Dozuki/CloudPrem-Infra/commit/e2499c3ac79f3924fe3ee82325ac08d8bb4cc947))

## [7.17.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.17.1...v7.17.2) (2026-07-26)


### Bug Fixes

* **logical:** make enable_bi actually work on a fresh deploy ([#286](https://github.com/Dozuki/CloudPrem-Infra/issues/286)) ([d3a0d72](https://github.com/Dozuki/CloudPrem-Infra/commit/d3a0d729acd6909127a181a3ecec4486dcc58fcd))
* **logical:** make enable_webhooks actually work ([#285](https://github.com/Dozuki/CloudPrem-Infra/issues/285)) ([d3906b4](https://github.com/Dozuki/CloudPrem-Infra/commit/d3906b44f5a3bfae1ff6b6d36d9955e3ce727d36))
* **logical:** raise the app helm timeout so a full deploy can converge ([#287](https://github.com/Dozuki/CloudPrem-Infra/issues/287)) ([97a4210](https://github.com/Dozuki/CloudPrem-Infra/commit/97a4210d42c0a938aacc7f93106d35d8de5f73e9))

## [7.17.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.17.0...v7.17.1) (2026-07-26)


### Bug Fixes

* **logical:** back the dashboards Grafana with MySQL, not SQLite ([61d15db](https://github.com/Dozuki/CloudPrem-Infra/commit/61d15db8ed2bcf81293dc6209f9d00c92c7865e7))

## [7.17.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.16.0...v7.17.0) (2026-07-25)


### Features

* **logical:** move frontegg DB-create and ztunnel PodMonitor into the chart ([#277](https://github.com/Dozuki/CloudPrem-Infra/issues/277)) ([10a99c6](https://github.com/Dozuki/CloudPrem-Infra/commit/10a99c69333fe1e0398bfbd670eec96194716fb0))
* **logical:** move the NLB TargetGroupBindings into the chart ([#278](https://github.com/Dozuki/CloudPrem-Infra/issues/278)) ([cceb810](https://github.com/Dozuki/CloudPrem-Infra/commit/cceb810e879f7cc49ba62a0dc954ade7ce393d4e))


### Bug Fixes

* **physical:** order the S3 batch replication job after its IAM policy ([#282](https://github.com/Dozuki/CloudPrem-Infra/issues/282)) ([e5b4505](https://github.com/Dozuki/CloudPrem-Infra/commit/e5b4505536d6efa7432ea7ac5e27ff4843f9e6eb))

## [7.16.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.15.0...v7.16.0) (2026-07-25)


### Features

* **physical:** delete the ElastiCache path, memcached is always in-cluster ([#275](https://github.com/Dozuki/CloudPrem-Infra/issues/275)) ([5acc323](https://github.com/Dozuki/CloudPrem-Infra/commit/5acc3235d9db4db4b0d572e5b63daa3e2ec60cea))

## [7.15.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.14.0...v7.15.0) (2026-07-25)


### Features

* **physical:** db_apply_immediately to override the maintenance-window default ([#272](https://github.com/Dozuki/CloudPrem-Infra/issues/272)) ([81817c9](https://github.com/Dozuki/CloudPrem-Infra/commit/81817c9754344075b6e3bbf339b296755487e7e4))
* **physical:** decouple the database CMK from enable_dr ([#274](https://github.com/Dozuki/CloudPrem-Infra/issues/274)) ([5fcc9a2](https://github.com/Dozuki/CloudPrem-Infra/commit/5fcc9a29cad37ec9dbd3188a54c328af4e0f2164))

## [7.14.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.13.0...v7.14.0) (2026-07-25)


### Features

* **logical:** additional exact-host gateway listeners ([#270](https://github.com/Dozuki/CloudPrem-Infra/issues/270)) ([f333235](https://github.com/Dozuki/CloudPrem-Infra/commit/f333235f764fbccec275c889f45a12240762dd8b))

## [7.13.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.12.0...v7.13.0) (2026-07-25)


### Features

* **logical:** add subsite_gateway_api_enabled to flip subsites to Gateway API ([#268](https://github.com/Dozuki/CloudPrem-Infra/issues/268)) ([d0ab222](https://github.com/Dozuki/CloudPrem-Infra/commit/d0ab22200c52f8a5076aa787755a0bded15a5b21))

## [7.12.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.11.1...v7.12.0) (2026-07-25)


### Features

* **logical:** istio ambient mesh behind istio_mesh_state ([#262](https://github.com/Dozuki/CloudPrem-Infra/issues/262)) ([445c490](https://github.com/Dozuki/CloudPrem-Infra/commit/445c4908ac95450b4fffbd6d82a5ba799ddc05f2))
* **logical:** pass the db resource id to the chart for migration keying ([#267](https://github.com/Dozuki/CloudPrem-Infra/issues/267)) ([9275844](https://github.com/Dozuki/CloudPrem-Infra/commit/927584427bc09231d2ccf79a133cd3f89f8d7e47))
* **logical:** seed the zendesk jwt into azure key vault ([37893b5](https://github.com/Dozuki/CloudPrem-Infra/commit/37893b5fe57a0568588e3490b6dd27d1ddf8a76f))


### Bug Fixes

* **logical:** post-cutover hardening — controller cpu, webnextjs env, dms-start ([#264](https://github.com/Dozuki/CloudPrem-Infra/issues/264)) ([f9932e2](https://github.com/Dozuki/CloudPrem-Infra/commit/f9932e21ddbfcde7d757181ef8de2b1bc2b924b3))

## [7.11.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.11.0...v7.11.1) (2026-07-23)


### Bug Fixes

* **physical:** dms replica parameter group missing on aurora stacks ([#260](https://github.com/Dozuki/CloudPrem-Infra/issues/260)) ([b2d4142](https://github.com/Dozuki/CloudPrem-Infra/commit/b2d414225315ec1f49a0f08a32217d3e1ca7b95a))

## [7.11.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.10.0...v7.11.0) (2026-07-22)


### Features

* **logical:** seed the azure service-jwt validation key, order kv seeding before helm ([27140f1](https://github.com/Dozuki/CloudPrem-Infra/commit/27140f16c90517d93bf6143977bf339a63cad356))


### Reverts

* back out the previous commit, its metadata was copied from the release commit ([132d4b3](https://github.com/Dozuki/CloudPrem-Infra/commit/132d4b361dea3ffe0d21f4ec238c53ca5f0423c4))

## [7.10.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.9.0...v7.10.0) (2026-07-17)


### Features

* **physical:** general log joins the default exports ([7b1cbfa](https://github.com/Dozuki/CloudPrem-Infra/commit/7b1cbfab61d3bae788a83718c04196fa2e341f2d))


### Bug Fixes

* **physical:** bastion AMI resolves via SSM at launch, not at plan time ([92620d9](https://github.com/Dozuki/CloudPrem-Infra/commit/92620d995fc14f7cbedebd5a9b8a149017087d3e))

## [7.9.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.8.1...v7.9.0) (2026-07-16)


### Features

* **physical:** default-on database log exports, 1y retention on new log groups ([158b83a](https://github.com/Dozuki/CloudPrem-Infra/commit/158b83abe0bb425e36e6520c8cf1880b6bf26e0f))

## [7.8.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.8.0...v7.8.1) (2026-07-16)


### Bug Fixes

* **physical:** bastion SSM associations target identity tags, not the whole tag map ([94ded20](https://github.com/Dozuki/CloudPrem-Infra/commit/94ded20ea0a6cfcaad64165807d0142d475fd160))

## [7.8.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.7.3...v7.8.0) (2026-07-16)


### Features

* **tags:** Service + Customer tags, one Identifier sentinel, casing advisories ([#252](https://github.com/Dozuki/CloudPrem-Infra/issues/252)) ([15681ca](https://github.com/Dozuki/CloudPrem-Infra/commit/15681cacf868ee59a60ea44877a5b08848f5aebd))

## [7.7.3](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.7.2...v7.7.3) (2026-07-15)


### Bug Fixes

* **physical:** drop the eks_cluster module-level depends_on ([#253](https://github.com/Dozuki/CloudPrem-Infra/issues/253)) ([80b6ba0](https://github.com/Dozuki/CloudPrem-Infra/commit/80b6ba0138473abc5c018ce5be6057874bcf243f))

## [7.7.2](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.7.1...v7.7.2) (2026-07-14)


### Bug Fixes

* **physical:** archive noncurrent guide-bucket versions instead of deleting ([#249](https://github.com/Dozuki/CloudPrem-Infra/issues/249)) ([6d67dce](https://github.com/Dozuki/CloudPrem-Infra/commit/6d67dcedc6d882220272b1d79bb7c9ebd467ae57))

## [7.7.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.7.0...v7.7.1) (2026-07-14)


### Bug Fixes

* **physical:** finops remediation for the infracost policy set ([#247](https://github.com/Dozuki/CloudPrem-Infra/issues/247)) ([0b971f2](https://github.com/Dozuki/CloudPrem-Infra/commit/0b971f2eec158d6d6ce9678ea4c02050e226afeb))

## [7.7.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.6.1...v7.7.0) (2026-07-09)


### Features

* **logical:** supplied TLS certs seed Vault from Terraform on AWS ([eb5c0ce](https://github.com/Dozuki/CloudPrem-Infra/commit/eb5c0ce040e9d01d9e9fa663d607ee6c1416dc20))

## [7.6.1](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.6.0...v7.6.1) (2026-07-09)


### ⚠ BREAKING CHANGES

* **logical:** service JWT key is unconditional; seed the azure nextjs KV secret

### Features

* **logical:** service JWT key is unconditional; seed the azure nextjs KV secret ([27b6098](https://github.com/Dozuki/CloudPrem-Infra/commit/27b60981145583d21724400759b9aeac26a1b5ba))


### Miscellaneous Chores

* **logical:** the unconditional jwt chart version is 1.9.0, not 2.0.0 ([41558fa](https://github.com/Dozuki/CloudPrem-Infra/commit/41558fa462ddedacb0ed0218b635677df330a2f5))

## [7.6.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.5.0...v7.6.0) (2026-07-08)


### Features

* **logical:** per-env web-nextjs env vars and service JWT toggle ([b751875](https://github.com/Dozuki/CloudPrem-Infra/commit/b751875b86a2e62919b4c13512371b0b003ce995))
* **logical:** seed ops-auth htpasswd for the chart's public ops ingress ([#242](https://github.com/Dozuki/CloudPrem-Infra/issues/242)) ([d58f05e](https://github.com/Dozuki/CloudPrem-Infra/commit/d58f05edb0570481514001799b7deea87ad17786))

## [7.5.0](https://github.com/Dozuki/CloudPrem-Infra/compare/v7.4.0...v7.5.0) (2026-07-06)


### Features

* **logical:** default chart_version to 1.7.1 ([#240](https://github.com/Dozuki/CloudPrem-Infra/issues/240)) ([e2398a9](https://github.com/Dozuki/CloudPrem-Infra/commit/e2398a9e33764b3dc11541645943b58b3934c52a))

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
