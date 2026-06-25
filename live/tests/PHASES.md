# Driving harness phases individually

The harness exposes four re-entrant phases via `cmd/harness`
(`provision` / `upgrade` / `validate` / `teardown`). Each is a separate process that
reconstructs run state from the **run manifest** in S3
(`<state-bucket>/<run-id>-<config>/harness-manifest.json`, alongside the Terraform
state) plus the live TF outputs. This is exactly how the Argo Workflow invokes each
step; locally you drive the same code via `run.sh PHASE=...`.

## Local usage

Reuse the **same `RUN_ID`** across all phases of one run:

```bash
RID=local-$(date +%s)

# Upgrade scenario
PHASE=provision SCENARIO_FLAG="--scenario upgrade" FROM_REF=v6.0.3 TO_REF=v7.1.0 \
  CONFIGS=min_default RUN_ID=$RID ./run.sh
PHASE=upgrade   CONFIGS=min_default RUN_ID=$RID ./run.sh
PHASE=validate  CONFIGS=min_default RUN_ID=$RID ./run.sh
PHASE=teardown  CONFIGS=min_default RUN_ID=$RID ./run.sh

# Fresh scenario (no upgrade step)
PHASE=provision SCENARIO_FLAG="--scenario fresh" TO_REF=auto:latest \
  CONFIGS=min_default RUN_ID=$RID ./run.sh
PHASE=validate  CONFIGS=min_default RUN_ID=$RID ./run.sh
PHASE=teardown  CONFIGS=min_default RUN_ID=$RID ./run.sh
```

- `KEEP_ON_FAILURE=1` on the `teardown` (or full) run leaves a failed stack up for
  live debugging instead of destroying it.
- `teardown` is idempotent and safe to re-run; with no manifest it is a no-op.
- The Vault tunnel + SSO checks in `run.sh` still apply in PHASE mode (each phase that
  applies/destroys terragrunt needs Vault), unless you preset `VAULT_TOKEN` /
  `SKIP_VAULT_TUNNEL=1`.

## Full-run parity (unchanged)

Omit `PHASE` to run the whole scenario via `go test` exactly as before:

```bash
SCENARIO=fresh CONFIGS=min_default ./run.sh
```

## Manifest fields (cross-phase state)

`scenario`, `from_ref`/`to_ref`, `delete_after` (TTL, set once at provision),
`applied_ref` (which ref's code matches the deployed state — drives teardown), and
`baseline_rev` (the pre-upgrade helm revision the upgrade proof checks against).
Everything else the validators need is re-derived from live TF outputs each phase.
