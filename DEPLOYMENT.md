# Deploying a Test Environment Stack

This guide covers deploying a new CloudPrem environment stack from a Mac workstation for development and testing.

## Prerequisites

### Required Tools

```bash
# Version managers for Terraform and Terragrunt
brew install tfenv tgenv

# Install the required versions
tfenv install 1.13.4
tfenv use 1.13.4
tgenv install 0.80.6
tgenv use 0.80.6

# Everything else
brew install awscli helm kubectl hashicorp/tap/vault
```

| Tool | Version | Verify |
|------|---------|--------|
| tfenv | latest | `tfenv --version` |
| tgenv | latest | `tgenv --version` |
| Terraform | 1.13.x (via tfenv) | `terraform version` |
| Terragrunt | 0.80.x (via tgenv) | `terragrunt --version` |
| AWS CLI | 2.x | `aws --version` |
| Helm | 3.x | `helm version` |
| kubectl | 1.28+ | `kubectl version --client` |
| Vault CLI | 1.15+ | `vault version` |

### AWS SSO Profiles

You need AWS SSO profiles configured in `~/.aws/config`. The profile name must match the `aws_profile` value in `account.hcl`.

```bash
# Login to the target account
aws sso login --profile <profile_name>

# Verify
aws sts get-caller-identity --profile <profile_name>
```

### Git Submodules

The Helm chart is a submodule. After cloning, initialize it:

```bash
git submodule update --init --recursive
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
  enable_vault                  = true       # Vault secret management via ESO
  enable_webhooks               = false      # MSK Kafka + Redis + Frontegg connectivity
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
| `min` | false | false | Core app + OpenSearch + monitoring |
| `hooks` | **true** | false | min + MSK Kafka + Redis + MongoDB + Frontegg connectivity |
| `bi` | false | **true** | min + BI replica DB + Grafana dashboards |
| `full` | **true** | **true** | Everything |

## Step 4: Configure Physical Inputs

Edit `physical/terragrunt.hcl` to add any required overrides:

```hcl
inputs = {
  # Required when enable_vault = true (get from vault-infrastructure deployment):
  vault_endpoint_service_name = "com.amazonaws.vpce.<region>.vpce-svc-xxxxxxxxxxxxxxxxx"
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

### Vault-Enabled Stacks (`enable_vault = true`)

The logical layer's Vault provider needs connectivity and authentication. Before running apply:

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
aws eks update-kubeconfig \
  --name dozuki-<env> \
  --region <region> \
  --profile <profile> \
  --alias dozuki-<env>

# Check all pods are running
kubectl --context dozuki-<env> get pods -n dozuki

# Check Helm releases
helm --kube-context dozuki-<env> list -A

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

**Warning:** If `protect_resources = true`, RDS and S3 resources will block deletion. Set it to `false` first and apply before destroying.

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

