# Deploying a Test Environment Stack

This guide covers deploying a new CloudPrem environment stack on **AWS** from a Mac
workstation for development and testing. For **Azure** deployments, use the
self-contained deploy kit and its runbook in [`azure-config/README.md`](./azure-config/README.md).

## Prerequisites

### Required Tools

```bash
# OpenTofu drives both layers locally — the physical layer requires >= 1.11.1, and
# OpenTofu runs the logical layer too. Terragrunt orchestrates them.
brew install opentofu terragrunt

# Everything else
brew install awscli helm kubectl hashicorp/tap/vault

# Point Terragrunt at OpenTofu (add this to your shell profile):
export TERRAGRUNT_TFPATH=tofu
```

| Tool | Version | Verify |
|------|---------|--------|
| OpenTofu | >= 1.11.1 | `tofu version` |
| Terragrunt | 0.99.x | `terragrunt --version` |
| AWS CLI | 2.x | `aws --version` |
| Helm | 3.x | `helm version` |
| kubectl | 1.28+ | `kubectl version --client` |
| Vault CLI | 1.15+ | `vault version` |

> **Toolchain note:** Production deploys run on **Spacelift**, which pins the
> *logical* layer to Terraform 1.5.7 (the last MPL-licensed release). The modules
> themselves only require Terraform/OpenTofu **>= 1.11.1** (physical — for the Aurora
> module and write-only attributes) and **>= 1.5.0** (logical), so a single OpenTofu
> binary runs both layers locally.

### AWS SSO Profiles

You need AWS SSO profiles configured in `~/.aws/config`. The profile name must match the `aws_profile` value in `account.hcl`.

```bash
# Login to the target account
aws sso login --profile <profile_name>

# Verify
aws sts get-caller-identity --profile <profile_name>
```

## Repository Structure

```
live/
├── terragrunt.hcl          # Root config (backend, providers)
├── common.hcl              # Logical layer dependency wiring
├── generate_live_env.sh    # Scaffold from skeletons
├── .skel/                  # Templates for new environments
├── standard/               # Standard AWS partition
│   ├── account.hcl         # Account ID + AWS profile
│   └── us-east-1/
│       ├── region.hcl
│       ├── min/            # Minimal environment
│       │   ├── env.hcl
│       │   ├── physical/terragrunt.hcl
│       │   └── logical/terragrunt.hcl
│       └── hooks/          # Webhooks-enabled environment
└── gov/                    # GovCloud partition
    ├── account.hcl
    └── us-gov-west-1/

terraform/
├── physical/               # AWS infrastructure (VPC, RDS, EKS, NLB, MSK)
└── logical/                # Kubernetes workloads (Helm, cert-manager, ESO)
```

## Step 1: Create the Live Environment

If the region directory doesn't exist yet, generate it from skeletons:

```bash
cd live/
./generate_live_env.sh
```

Or manually copy a skeleton environment:

```bash
cp -R .skel/environments/min live/standard/us-east-1/min
```

## Step 2: Configure Account

Edit `live/standard/<region>/../../account.hcl` (or `live/gov/.../account.hcl`):

```hcl
locals {
  aws_account_id = "123456789012"    # Target AWS account
  aws_profile    = "your-profile"    # AWS CLI SSO profile name
}
```

Ensure `region.hcl` exists:

```hcl
locals {
  aws_region = "us-east-1"
}
```

## Step 3: Configure Environment Variables

Edit `env.hcl` for your environment:

```hcl
locals {
  environment                   = "min"
  enable_webhooks               = false      # MSK Kafka for webhooks
  enable_bi                     = false      # BI replica database + Grafana dashboards
  rds_multi_az                  = false      # Multi-AZ RDS (production only)
  highly_available_nat_gateway  = false      # NAT per AZ (production only)
  protect_resources             = false      # Deletion protection (production only)
  alarm_email                   = "your-team@example.com"
  image_tag                     = "abc123.1" # Required: app Docker image tag
  nextjs_tag                    = "2.3.0"    # Required: Next.js frontend tag
}
```

`image_tag` and `nextjs_tag` are **required** — the deploy will fail without them. Get the current tags from an existing environment or from the CI build.

### Environment Types

| Type | `enable_webhooks` | `enable_bi` | What it deploys |
|------|:-:|:-:|---|
| `min` | false | false | Core application stack + monitoring |
| `hooks` | **true** | false | Core + webhooks infrastructure (MSK Kafka) |
| `bi` | false | **true** | Core + business-intelligence replica database |
| `full` | **true** | **true** | All features (webhooks + BI); pair with multi-AZ + DR for production |

## Step 4: Configure Physical Inputs

Edit `physical/terragrunt.hcl` to add any required overrides:

```hcl
inputs = {
  # Required — Vault is mandatory (get the PrivateLink service name from the
  # central Vault deployment):
  vault_endpoint_service_name = "com.amazonaws.vpce.<region>.vpce-svc-xxxxxxxxxxxxxxxxx"

  # Optional — names this stack and its subdomain. Resources are named
  # "<customer>-<env>"; defaults to "dozuki-<env>" when unset.
  # customer = "acme"

  # Optional — "rds" (default, provisioned MySQL) or "aurora" (Serverless v2).
  # db_engine = "aurora"
}
```

## Step 5: Deploy

From the environment root, deploy both layers:

```bash
cd live/standard/us-east-1/<env>
terragrunt run-all apply
```

Terragrunt automatically applies physical before logical based on the dependency declared in `common.hcl`. The full deploy takes **20-40 minutes** (EKS ~12 min, RDS ~10 min, MSK ~20 min if webhooks enabled).

### Credential Workaround

If you get backend configuration errors, you may need to export credentials explicitly:

```bash
eval "$(aws configure export-credentials --profile <profile> --format env)"
TG_AWS_PROFILE='' AWS_PROFILE='' terragrunt run-all apply
```

> **Heads-up:** these exported `AWS_*` env vars expire (~1 hour) and **override**
> `AWS_PROFILE` while set — once they expire, profile-based commands fail with
> confusing auth errors even though your SSO session is valid. Run
> `unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN` when you're done
> with the workaround.

### Vault Setup (required)

Vault is mandatory: the logical layer authenticates to the central Vault and seeds
this stack's secrets. Its provider needs connectivity and authentication before apply:

**1. Start a tunnel to Vault** (keep this running in a separate terminal):

```bash
# Via kubectl (if you have access to the vault cluster):
kubectl --context <vault-context> port-forward -n vault svc/vault-active 8200:8200

# Via SSM (if using bastion):
aws ssm start-session \
  --target $(aws ec2 describe-instances \
    --filters "Name=tag:Name,Values=<vault-bastion-tag>" "Name=instance-state-name,Values=running" \
    --query 'Reservations[0].Instances[0].InstanceId' \
    --output text --region <vault-region> --profile <vault-account-profile>) \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters '{"host":["<vault-nlb-ip>"],"portNumber":["8200"],"localPortNumber":["8200"]}' \
  --region <vault-region> --profile <vault-account-profile>
```

Get the NLB private IP from the vault-infrastructure deployment or via:
```bash
aws ec2 describe-network-interfaces --filters "Name=description,Values=*vault-nlb*" \
  --query 'NetworkInterfaces[0].PrivateIpAddress' --output text \
  --region <vault-region> --profile <vault-account-profile>
```

**2. Authenticate to Vault:**

```bash
eval "$(aws configure export-credentials --profile <vault-account-profile> --format env)"
VAULT_ADDR=http://127.0.0.1:8200 vault login -method=aws role=<your-vault-role>
```

The Vault CLI doesn't support AWS SSO profiles natively — the `eval` export is required.

**3. Deploy with Vault address set:**

```bash
eval "$(aws configure export-credentials --profile <target-account-profile> --format env)"
export VAULT_ADDR=http://127.0.0.1:8200
TG_AWS_PROFILE='' AWS_PROFILE='' terragrunt run-all apply
```

## Step 6: Verify

Set up kubectl and check the deployment:

```bash
# The cluster name is "<customer>-<env>" (or "dozuki-<env>" when customer is unset).
CLUSTER=dozuki-<env>
aws eks update-kubeconfig \
  --name "$CLUSTER" \
  --region <region> \
  --profile <profile> \
  --alias "$CLUSTER"

# Check all pods are running
kubectl --context "$CLUSTER" get pods -n dozuki

# Check Helm releases
helm --kube-context "$CLUSTER" list -A

# Check the app URL (from Terraform output)
cd logical && terragrunt output dozuki_url
```

## Skipping Layers

Use environment variables to skip a layer:

```bash
SKIP_LOGICAL=true terragrunt run-all apply    # Physical only
SKIP_INFRA=true terragrunt run-all apply      # Skip everything (both layers)
```

## Tearing Down

Destroy in reverse order:

```bash
cd live/standard/us-east-1/<env>/logical
terragrunt destroy

cd ../physical
terragrunt destroy
```

Or from the environment root:

```bash
terragrunt run-all destroy
```

**Warning:** If `protect_resources = true`, RDS, S3, and the NLB get deletion
protection and will block `destroy` — and a protected NLB keeps ENIs that pin the
internet gateway, hanging the whole VPC teardown. Set `protect_resources = false` and
apply once *before* destroying.

## Troubleshooting

### Backend configuration changed
```bash
TF_CLI_ARGS_init='-reconfigure' terragrunt apply
```

### State lock stuck
Find the lock ID from the error message, then:
```bash
cd <layer>/.terragrunt-cache/*/logical  # or physical
terraform force-unlock -force <LOCK_ID>
```

### Vault provider "failed to configure Vault address"
Ensure `VAULT_ADDR` is exported and the tunnel is running:
```bash
curl -s http://127.0.0.1:8200/v1/sys/health | jq .
```

### Vault "permission denied / invalid token"
Re-authenticate:
```bash
eval "$(aws configure export-credentials --profile <vault-account-profile> --format env)"
VAULT_ADDR=http://127.0.0.1:8200 vault login -method=aws role=<your-vault-role>
```
Token expires after 1 hour.

