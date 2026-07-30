# The harness container image

One image, one binary, four subcommands. `live/tests/Dockerfile` builds the runtime for
the harness phases so they can run as pods on the Argo ops cluster instead of only from a
laptop. `run.sh` stays the local launcher and drives the identical subcommands, so a
container run and a laptop run remain comparable.

## Build

From the **repo root** (the provider warm stage needs `terraform/`):

```bash
docker buildx build --platform linux/amd64 -f live/tests/Dockerfile -t cpi-harness:dev --load .
```

`--platform linux/amd64` is not optional. A plain build on an arm64 Mac stamps arm64 and
emits an OCI index EKS cannot pull, and `docker inspect .Architecture` reports the host
arch, so it is not proof. Check `uname -m` inside the container or the registry manifest.

For a pushed image add `--provenance=false --sbom=false` to get a single plain manifest
rather than a manifest list with attestations.

## Run

```bash
docker run --rm --platform linux/amd64 \
  -e HARNESS_REPO_REF=master \
  -e VAULT_ADDR=http://vault.internal.dozuki.com:8200 \
  cpi-harness:dev provision --run-id <id> --config min_default \
    --scenario upgrade --from-ref v8.3.0 --to-ref v8.3.1 \
    --account-id 076248559428 --state-bucket <bucket>
```

The entrypoint is the server-side half of `run.sh`. It arms the az-shim, clones or fetches
the repo, mints the manual-TLS cert, logs into Vault with the pod's own identity, prints
the toolchain, then execs the phase. Anything that is not a phase name is exec'd as given,
so `docker run ... bash` and `docker run ... cpi-dr ...` both work.

| env | default | why |
|---|---|---|
| `HARNESS_REPO_DIR` | `/workspace/repo` | Mount as a volume shared by every phase of one run and the clone becomes an incremental fetch |
| `HARNESS_REPO_REF` | `master` | The ref the harness itself runs from, not the ref under test |
| `HARNESS_REPO_URL` | GitHub CPI | |
| `VAULT_ADDR` | unset | Unset skips the Vault login; every terragrunt apply and destroy needs Vault |
| `VAULT_AWS_ROLE` | `admin` | Vault AWS-auth role for the pod identity |
| `VAULT_AWS_REGION` | unset | Needed for cross-partition STS (GovCloud signs for `us-gov-west-1`) |
| `SKIP_VAULT_LOGIN` | `0` | For offline testing |
| `NO_AZ_SHIM` | `0` | Drops the az-shim from PATH |

## Why the image is 2.4GB, and what that buys

The harness runs terragrunt once per layer per git worktree, so an upgrade scenario pays
`tofu init` four or more times, each resolving ~12 providers. Nothing in the repo
configures a provider cache today, so every one of those inits downloads the set again.
The image bakes a plugin cache warmed from both layers' committed lockfiles.

Measured in-container on the logical layer, clean working dir, linux/amd64:

| | wall time | disk written |
|---|---|---|
| `tofu init` with the baked cache | **6.6s** | **148KB** (11 symlinks into the cache) |
| `tofu init` without | 85s | 1.3GB |

Every provider symlinks to `/opt/tofu-plugin-cache`, so the cache costs disk once in the
image rather than per worktree. Four inits per upgrade run is roughly **5 minutes and 5GB
of writes saved per run**, and the cold figure came off a fast connection, so a pod
egressing through NAT to the public registry will do worse.

The cache is best-effort on purpose. It is keyed by provider version and the harness also
tests old refs, whose lockfiles may pin versions the cache lacks. A miss falls back to a
normal download: a stale cache costs time, never correctness.

## Size breakdown and the next cuts

Uncompressed rootfs is 2.4GB:

| area | size | note |
|---|---|---|
| provider cache | 1.3GB | the deliberate trade above |
| **`vault` CLI** | **537MB** | used for exactly two things: `vault login -method=aws` and `vault token renew` |
| `python3` + `aws-cli` | ~180MB | used by four Go call sites |
| tofu / terragrunt / helm / kubectl | 315MB | |
| `harness` + `cpi-dr` | 96MB | |
| alpine base + packages | ~30MB | |

Two follow-ups would take this to roughly **1.7GB**, both replacing a CLI with API calls
the harness already has the libraries for:

1. **Drop the `vault` CLI (-537MB).** `vault login -method=aws` and `vault token renew`
   are two HTTP calls. The binary is 537MB because it ships the whole Vault server and UI;
   there is no CLI-only build. This is the single most wasteful thing in the image, larger
   than every other tool combined. Touches `harness/vault.go` and the entrypoint.
2. **Drop the `aws` CLI (-180MB).** Four call sites remain: `eks update-kubeconfig`
   (`validation/cluster.go`), `elbv2 describe-load-balancers` and
   `modify-load-balancer-attributes` (`harness/terragrunt.go`), and
   `configure export-credentials` (local-profile path only, unused in-cluster).

Alpine is the base specifically because its community `aws-cli` is musl-native; the
official v2 installer is glibc-only, which would otherwise force Debian and ~230MB more.

An alternative to baking the cache is holding it on a volume the workflow mounts, warmed
once. That would also cover old refs' provider versions and shrink the image to ~1.1GB, at
the cost of a volume to manage and warm. Baked is simpler and pulls once per node.

## Registry

Publish to the **dozukicloud** ECR (069174876992), which already holds the image and chart
repos and already has GitHub-OIDC release roles, and grant the Argo cluster cross-account
pull through the existing `dozukicloud-ecr-policies` stack.

Deliberately not an ECR repo in the management account: that would be a marginally faster
same-account pull, but it means trusting GitHub's OIDC provider inside the account that
holds Vault. The existing precedent keeps GitHub-trusted roles in sandbox and platform
accounts (`cloudprem-test-harness` in DDVtest, `dozuki-operator-cicd` in dozukicloud), and
this image is not worth breaking that.

Still to wire: the ECR repo itself, a pull-policy entry for the Argo cluster's pod
identity, and a build-and-push workflow pinning the image by digest in the
WorkflowTemplates.
