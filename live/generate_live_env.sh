#!/usr/bin/env bash

PARTITIONS_PATH=../.skel/partitions

for partition in standard gov; do
  if [ -d "$partition" ]; then
    rm -fr "$partition"
  fi
  mkdir $partition
  pushd $partition || exit
  if [ -f $PARTITIONS_PATH/$partition/account.hcl ]; then
    cp $PARTITIONS_PATH/$partition/account.hcl .
  else
    cat << EOF > $PARTITIONS_PATH/$partition/account.hcl
locals {
  aws_account_id = "123456789"
  aws_profile    = "your_profile"
}
EOF
    echo "Update $PARTITIONS_PATH/$partition/account.hcl to reflect your test account info and re-run this script."
    exit
  fi
  while IFS= read -r region
  do
      echo "Creating live environments for $region"
      if [ -d "$region" ]; then
        rm -fr "$region"
      fi
      mkdir "$region"
      pushd "$region" || exit
        cp -R ../../.skel/environments/* .
        cat << EOF > region.hcl
locals {
  aws_region = "$region"
}
EOF
      popd || exit
  done < <(grep -v '^ *#' < $PARTITIONS_PATH/$partition/regions.txt)
  popd || exit
done