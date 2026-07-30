# Running the harness on Argo Workflows

Manifests for running the harness phases as pods on the Argo ops cluster
(`argo-ops`, management account 010601635461). `run.sh` remains the local launcher and
drives the identical `harness <phase>` subcommands, so the two paths stay comparable.

```
00-phase-templates.yaml   config, semaphore, and the harness-phase template (one per subcommand)
10-scenario.yaml          harness-scenario: one config end to end, teardown as an exit handler
20-matrix.yaml            harness-matrix: fans out over configs, submits one child workflow each
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

**Teardown is an exit handler.** A final step does not run when an earlier step fails,
which is precisely when teardown matters. And because teardown reads the manifest from S3,
cleanup is not tied to the process that created the stack: this pod, a retry, or the Phase 4
janitor can all clean up a given run. That is the structural fix for the trap in `run.sh`.

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
  and `recover_source` configs stay on `run.sh` until the CLI grows a phase for them.
- **Cron and PR triggers**, and the backstop janitor. Those are Phase 4. Everything here is
  submit-driven so far.
