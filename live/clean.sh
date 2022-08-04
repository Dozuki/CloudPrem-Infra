#!/bin/bash

prefixes=("default" "ha" "vpn" "havpn")
envs=("min" "bi" "hooks" "full")

cleanTerraFiles() {
    find . -type d -name ".terragrunt-cache" -prune -exec rm -rf {} \;
}

cleanPrefixes() {
    for p in ${prefixes[@]}; do
        echo "Cleaning prefix $p"
        cleanTerraFiles
#         for region in $(ls); do
#              if [[ $region != "account.hcl" ]]; then
#                 echo "Clearing k8 auth from state for $region"
#                 pushd $region
#                 for env in ${envs[@]}; do
#                     echo "Clearing $env"
#                     pushd $env/physical
#                     TG_STATE_PREFIX="test/$p/" terragrunt state rm "module.eks_cluster.kubernetes_config_map.aws_auth[0]"
#                     popd
#                 done
#                 popd
#              fi
#         done
        SKIP_LOGICAL=true TG_STATE_PREFIX="test/$p/" terragrunt run-all destroy --terragrunt-non-interactive
    done
}
# echo "Cleaning standard"
# pushd standard
# cleanPrefixes
# popd
# pushd gov
# cleanPrefixes
# popd


for region in $(ls standard/); do
 if [[ $region != "account.hcl" ]]; then
    echo "Cleaning $region"
    awsweeper --region $region --force tests/nuke.yaml

 fi
done

echo "Cleaning Gov"
awsweeper --region us-gov-west-1 --profile gov --force tests/nuke.yaml

cloud-nuke aws --exclude-resource-type cloudwatch-loggroup --exclude-resource-type lambda --exclude-resource-type iam-role --force --config tests/cloud-nuke.yaml
# AWS_PROFILE=gov cloud-nuke aws --region us-gov-west-1 --exclude-resource-type cloudwatch-loggroup --exclude-resource-type lambda --exclude-resource-type iam-role --force --config tests/cloud-nuke.yaml
