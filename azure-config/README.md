# Dozuki MPC on Azure — Deploy Kit

Self-contained deployment bundle for Dozuki MPC (managed private cloud) on Azure (AKS).

## Prerequisites
- Azure CLI (`az`) installed and able to reach your subscription
- Subscription role: Owner (or Contributor + Role Based Access Control Administrator)
- An Azure Container Registry in your subscription
- An Entra security group for cluster admins (deploying user must be a member)
- Outbound HTTPS to: github.com, ghcr.io, releases.hashicorp.com, dl.k8s.io, get.helm.sh

## Quick start
1. `cp physical.tfvars.example physical.tfvars` and fill in the REQUIRED values.
2. `cp logical.tfvars.example logical.tfvars` and fill in the REQUIRED values.
3. `./bootstrap.sh all`

Phases run in order: `init` (tools, login, remote state), `physical`
(~30-45 min), `sync-images`, `logical` (~15-30 min). Every phase is safe to
rerun; if a call drops mid-apply, run the same phase again.

Individual phases: `./bootstrap.sh init|physical|sync-images|logical|status`

## After deploy
- `./status.sh` prints cluster, database, and application health.
- Point a DNS A record for your `external_fqdn` at the LoadBalancer IP shown by
  `./bootstrap.sh status`.
- `status` and `status.sh` assume an authenticated `az` session; run
  `./bootstrap.sh init` first on a fresh shell.

## Upgrades
Download the new bundle, copy your `*.tfvars` files in, rerun
`./bootstrap.sh all`.
