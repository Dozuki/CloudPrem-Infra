# Running the harness on Argo Workflows

Manifests for running the harness phases as pods on the Argo ops cluster
(`argo-ops`, the management account). `run.sh` remains the local launcher and
drives the identical `harness <phase>` subcommands, so the two paths stay comparable.

```
00-phase-templates.yaml   config, semaphore, and the harness-phase template (one per subcommand)
10-scenario.yaml          harness-scenario: one config end to end, teardown as an exit handler
20-matrix.yaml            harness-matrix: fans out over configs, submits one child workflow each
40-nightly-cron.yaml      the nightly clock for harness-matrix
50-janitor-cron.yaml      report-only janitor + Resource Reaper controlled action worker
```

Apply:

```bash
aws eks update-kubeconfig --name argo-ops --region us-east-1 --profile dozuki --alias argo-ops
kubectl --context argo-ops -n argo apply -f live/tests/argo/
```

Submit:

```bash
# one config, a specific target ref (what a PR run does)
argo submit -n argo --from workflowtemplate/harness-matrix \
  -p configs='["min_default"]' -p to-ref=<sha>

# the full nightly matrix
argo submit -n argo --from workflowtemplate/harness-matrix

# a fresh-deploy run instead of an upgrade
argo submit -n argo --from workflowtemplate/harness-scenario \
  -p config=min_default -p scenario=fresh -p to-ref=auto:latest
```

## Two prerequisites that are NOT wired yet

**These templates cannot run until both exist.** Both need the Argo workflow role ARN, which
only exists once the `ArgoOpsCluster` stack has applied (`argo_pod_identity_role_arn`).

1. **The DDVtest role's trust policy must admit the Argo workflow role.** The role itself
   already exists: `cloudprem-upgrade-tests`, created by infra-tf
   `modules/cloudprem-test-harness` and currently trusted only by GitHub's OIDC provider.
   Extending its trust is a smaller change than minting a second role, and it keeps one
   identity for "the thing that drives DDVtest".
2. **The Argo workflow role must be registered as a Vault AWS-auth role.** Every terragrunt
   apply and destroy reads Vault. This lives in infra-tf `vault-config`, which is
   deliberately not a Spacelift stack and is applied by hand over a port-forward.

## How the pieces fit

**Re-entrancy is the point.** Everything a phase needs derives from `--run-id` +
`--config`; state lives in the S3 run manifest beside the Terraform state. A phase that
dies is retried and rebuilds its context, instead of a failure at 1h50m costing the whole
run. The 2026-07-29 interrupt is the case in point: it threw away six minutes of EKS create
with no way to resume, and its trap-based teardown failed and needed a separate manual
sweep.

**`retryPolicy: OnError`, not `OnFailure`.** OnError covers the pod dying (eviction, OOM,
spot reclaim, node replacement), which re-entrancy makes safe to repeat. A non-zero exit
from the harness is a real verdict; retrying it would burn another hour and report the same
thing.

**Teardown is the exception, and carries its own retry.** OnError is correct for provision
and validate because their exit code IS the verdict. It is wrong for teardown: a non-zero
exit there is a failed cleanup, not an answer, and the stack it failed to destroy sits on
DDVtest's books against a ceiling of 10 VPCs until a human notices. So the `teardown` step
in `10-scenario.yaml` carries its own `retryPolicy: Always`, on the exit-handler template
rather than the shared one, so provision and validate are untouched. The budget is bounded
on purpose (three attempts, wall-clock capped): the workflow holds `harness-semaphore`
through the whole exit handler, so an unbounded retry on one run would starve the other
slot instead of just leaking a VPC.

**Teardown is an exit handler.** A final step does not run when an earlier step fails,
which is precisely when teardown matters. And because teardown reads the manifest from S3,
cleanup is not tied to the process that created the stack: this pod, a retry, or the Phase 4
janitor can all clean up a given run. That is the structural fix for the trap in `run.sh`.

**The janitor is the backstop for a teardown that doesn't retry its way out.** The exit
handler above plus its own retry (this same `run` template, `retryPolicy: OnError`) covers
a destroy that fails transiently. Neither covers a destroy that fails for a reason no retry
fixes - a state lock held by a dead process, expired credentials, a pod killed between apply
and the manifest write. Those abandon a stack permanently: seven of them leaking over three
days exhausted DDVtest's 10-VPC ceiling and took the nightly down with VpcLimitExceeded.
`harness-janitor` (`50-janitor-cron.yaml`) runs nightly, never matches on a name, always
uses `--sweep=false`, and publishes Engine Report v1 to Resource Reaper. The separate
`harness-reaper-worker` is installed suspended during shadow validation. Once enabled,
it checks its FIFO every five minutes and starts the heavy executor
only when actions are enabled and a message exists. That executor consumes at most one
durable Reaper action, repeats the full scan/version/ownership checks, and installs a
final control-plane veto immediately before the existing selected sweep path. There is
no Argo parameter that bypasses Reaper. See `harness/janitor.go` and
`harness/reaper_worker.go` for the enforced predicates and their test coverage.

**A sweep cleans more than the terragrunt destroy.** `PhaseParams.Teardown` runs the same
destroy every phase pod's exit handler runs, but a sweep also carries the out-of-band
cleanup `cleanup-orphans.sh` used to do by hand (that script is now the porting spec, not
a second implementation - see its own header for what stays script-only). Before the
physical destroy: clearing NLB deletion protection and deleting any MSK cluster
Terraform's state lost track of (both would otherwise strand the VPC on a
DependencyViolation), and clearing the S3 backend's stale `-md5` state digest so an
interrupted prior apply can't abort the destroy before it starts. After every successful
destroy, sweep or not: reclaiming CSI-created EBS volumes and launch templates, the
lambda/DMS log groups that only exist lazily at runtime, and the flux-source-controller
IAM role that otherwise collides with the next run's `CreateRole`. And when a destroy
succeeds but a tag re-query still finds something standing (`StateResidue`): a targeted,
single-call delete for exactly the ARNs that query returned, never a wider search. Every
mutation sits on a path only `Sweep` (or, for the always-on post-destroy reclaim, a real
`Teardown`) can reach, so `--sweep=false` report mode never mutates anything.

**One semaphore across every entry point.** DDVtest allows 10 VPCs per region and a test
stack is about one (recovery is two). A nightly matrix plus a couple of PR runs will
exhaust that mid-apply and surface as a confusing quota error inside a terragrunt run. The
children hold the semaphore for their whole life, provision through teardown. The
orchestrator deliberately does not take it: gating both would deadlock any matrix larger
than the semaphore.

**Child workflows, not child templates.** The fan-out costs a resource template and the
RBAC in `20-matrix.yaml`, and buys per-config isolation: own UI, own retry budget, own
resume scope, own exit handler. "Re-run just `full`" is resubmitting one child.

**Provider cache.** Phases mount no plugin cache volume because they do not need one: the
image bakes it and `.terraform/providers` symlinks into it. See `../IMAGE.md` for the
measurements.

## Not covered

- **The recovery scenario.** `cmd/harness` exposes `provision/upgrade/validate/teardown`
  with `--scenario upgrade|fresh`; `RunRecovery` has no phase equivalent, so the `recover`
  and `recover_source` configs stay on `run.sh` until the CLI grows a phase for them. The
  janitor's own region-mismatch check (`harness/janitor.go`) keeps it from ever sweeping
  the `recover` config's DR-region rebuild for the same reason - it reports `needs-review`
  rather than guess at a destroy this CLI cannot yet drive.
