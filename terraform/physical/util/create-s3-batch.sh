#!/usr/bin/env bash
# Create an S3 batch replication job for objects eligible for (or failed) replication,
# writing task reports to the logging bucket.
#
# Usage: ./create-s3-batch.sh <LOGGING_BUCKET> <SOURCE_BUCKET> <REPLICATION_ROLE_ARN> <ACCOUNT> <PARTITION> [AWS_PROFILE]
# PARTITION is one of aws, aws-cn, aws-us-gov.

set -euo pipefail

AWS_LOGGING_BUCKET="$1"
AWS_SOURCE_BUCKET="$2"
AWS_REPLICATION_ROLE="$3"
AWS_ACCOUNT="$4"
AWS_PARTITION="$5"
# AWS_PROFILE in $6

AWS_PREFIX=""

if [ "${6:-}" != "" ]; then
  AWS_PREFIX="AWS_PROFILE=$6"
fi

sleep 30 # Cheap insurance against a race condition caused by the batch job being created before the IAM role is ready.

$AWS_PREFIX aws s3control create-job \
  --account-id "$AWS_ACCOUNT" \
  --operation '{"S3ReplicateObject":{}}' \
  --description "Source $AWS_SOURCE_BUCKET" \
  --report "{\"Bucket\":\"$AWS_LOGGING_BUCKET\",\"Prefix\":\"batch-replication-report\", \"Format\":\"Report_CSV_20180820\",\"Enabled\":true,\"ReportScope\":\"AllTasks\"}" \
  --manifest-generator "{\"S3JobManifestGenerator\": {\"SourceBucket\": \"arn:$AWS_PARTITION:s3:::$AWS_SOURCE_BUCKET\", \"EnableManifestOutput\": false, \"Filter\": {\"EligibleForReplication\": true}}}" \
  --priority 1 \
  --role-arn "$AWS_REPLICATION_ROLE" \
  --no-confirmation-required