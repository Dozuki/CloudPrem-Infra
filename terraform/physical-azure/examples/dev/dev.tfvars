# Dev/smoke-test values for Azure commercial cloud.
# Usage (requires an Azure subscription + `az login`):
#   terraform -chdir=terraform/physical-azure init -backend=false
#   terraform -chdir=terraform/physical-azure plan -var-file=examples/dev/dev.tfvars -var subscription_id=$ARM_SUBSCRIPTION_ID

azure_environment          = "public"
location                   = "eastus2"
customer                   = "azdev"
environment                = "dev"
protect_resources          = false
external_fqdn              = "azdev.dozuki.com"
alarm_email                = "devops@dozuki.com"
mysql_high_availability    = false
mysql_geo_redundant_backup = false
aks_node_count_min         = 2
aks_node_count_max         = 3
