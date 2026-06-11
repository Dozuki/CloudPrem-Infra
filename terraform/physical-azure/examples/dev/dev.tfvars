# Dev/smoke-test values for Azure commercial cloud.
# Usage (requires an Azure subscription + `az login`):
#   terraform -chdir=terraform/physical-azure init -backend=false
#   terraform -chdir=terraform/physical-azure plan -var-file=examples/dev/dev.tfvars -var subscription_id=$ARM_SUBSCRIPTION_ID
#
# kv_allowed_cidrs is effectively REQUIRED for apply: the Key Vault firewall
# denies by default, and seeding the database secret needs your egress IP
# (e.g. `curl -s ifconfig.me`). Plan works without it; apply will not.

azure_environment = "public"
location          = "eastus2"
customer          = "azdev"
environment       = "dev"
protect_resources = false
external_fqdn     = "azdev.dozuki.com"
alarm_email       = "devops@dozuki.com"
# kv_allowed_cidrs   = ["203.0.113.4"] # your deployment egress IP — see header
mysql_high_availability    = false
mysql_geo_redundant_backup = false
aks_node_count_min         = 2
aks_node_count_max         = 3
