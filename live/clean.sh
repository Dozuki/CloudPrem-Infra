#!/bin/bash
set -euo pipefail

prefixes=("default" "ha" "vpn" "havpn")
envs=("min" "bi" "hooks" "full")

cleanTerraFiles() {
    find . -type d -name ".terragrunt-cache" -prune -exec rm -rf {} \;
}

cleanK8Auth() {
  for region in *; do
    if [[ $region != "account.hcl" ]]; then
      echo "Clearing k8 auth from state for $region"
      pushd "$region" || exit
      for env in "${envs[@]}"; do
        echo "Clearing $env"
        pushd "$env/physical" || exit
        TG_STATE_PREFIX="test/$p/" terragrunt state rm "module.eks_cluster.kubernetes_config_map.aws_auth[0]"
        popd || exit
      done
      popd || exit
    fi
  done
}

cleanPrefixes() {
    for p in "${prefixes[@]}"; do
        echo "Cleaning prefix $p"
        cleanTerraFiles
        # If you're having issues with stuck kubernetes auth configmaps (which happens sometimes on an un-clean delete)
        # uncomment this function invocation and re-run the script.
        #cleanK8Auth
        SKIP_LOGICAL=true TG_STATE_PREFIX="test/$p/" terragrunt run-all destroy --terragrunt-non-interactive
    done
}

if [ ! -d standard ] || [ ! -d gov ]; then
  ./generate_live_env.sh
fi

echo "Cleaning standard"
pushd standard || exit
#cleanPrefixes
for region in *; do
 if [ -d "$region" ]; then
    echo "Cleaning $region"
    awsweeper --region "$region" --force ../tests/awsweeper.yaml

 fi
done
cloud-nuke aws --exclude-resource-type cloudwatch-loggroup --exclude-resource-type lambda --exclude-resource-type iam-role --force --config ../tests/cloud-nuke.yaml
popd || exit

# echo "Cleaning gov"
# pushd gov || exit
# cleanPrefixes
# awsweeper --region us-gov-west-1 --profile gov --force ../tests/awsweeper.yaml
# AWS_PROFILE=gov cloud-nuke aws --region us-gov-west-1 --exclude-resource-type cloudwatch-loggroup --exclude-resource-type lambda --exclude-resource-type macie-member --force --config ../tests/cloud-nuke.yaml
popd || exit




