#!/usr/bin/env bash
set -euo pipefail

# Iterate over both standard and gov partitions
for partition in standard gov; do
  # Remove existing partition directory, if any, and create a new one
  if [ -d "$partition" ]; then
    rm -fr "$partition"
  fi
  mkdir "$partition"

  # Move into the partition directory
  pushd "$partition" >/dev/null || exit

  # Copy the account.hcl file for the partition
  cp ../.skel/partitions/$partition/account.hcl .

  # Read the list of regions for the partition from the regions.txt file
  while IFS= read -r region
  do
      echo "Creating live environments for $region"

      # Remove existing region directory, if any, and create a new one
      if [ -d "$region" ]; then
        rm -fr "$region"
      fi
      mkdir "$region"

      # Move into the region directory
      pushd "$region" >/dev/null || exit

        # Copy the environment files from the .skel directory
        cp -R ../../.skel/environments/* .

        # Create a region.hcl file with the region-specific information
        cat << EOF > region.hcl
locals {
  aws_region = "$region"
}
EOF

      # Move back to the partition directory
      popd >/dev/null || exit
  done < <(grep -v '^ *#' < ../.skel/partitions/$partition/regions.txt)

  # Move back to the initial directory
  popd >/dev/null || exit
done
