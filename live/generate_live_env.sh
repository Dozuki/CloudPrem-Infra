#!/usr/bin/env bash

for partition in standard gov; do
  if [ -d "$partition" ]; then
    rm -fr "$partition"
  fi
  mkdir $partition
  pushd $partition || exit
  cp ../.skel/partitions/$partition/account.hcl .
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
  done < <(grep -v '^ *#' < ../.skel/partitions/$partition/regions.txt)
  popd || exit
done