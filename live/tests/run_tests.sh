#!/bin/bash

date=$(date +%Y-%m-%d-%T)

cd src
go test -v -count 1 -timeout 3000m |terratest_log_parser --outputdir "test_output-${date}"
cd ../../
./clean.sh
cd tests
say "tests done"