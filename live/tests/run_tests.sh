#!/bin/bash

date=$(date +%Y-%m-%d-%T)

# Ensure the live environment exists
if [ ! -d ../standard ] || [ ! -d ../gov ]; then
  pushd ../
  ./generate_live_env.sh || exit
  popd || exit
fi
# Clear all existing terragrunt config/terraform files in live environment
find ../ -name '.terra*' -exec rm -fr {} \;
cd src || exit
go test -v -count 1 -timeout 3000m |terratest_log_parser --outputdir "test_output-${date}"
cd ../../
./clean.sh
cd tests || exit
say "tests done"