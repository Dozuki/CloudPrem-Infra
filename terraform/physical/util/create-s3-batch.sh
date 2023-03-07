#!/usr/bin/env bash

set -euo pipefail

AWS_LOGGING_BUCKET="$1"
AWS_SOURCE_BUCKET="$2"
AWS_REPLICATION_ROLE="$3"
AWS_PROFILE="$4"
AWS_ACCOUNT="$5"

aws s3control create-job \
  --account-id "$AWS_ACCOUNT" \
  --profile "$AWS_PROFILE" \
  --operation '{"S3ReplicateObject":{}}' \
  --report "{\"Bucket\":\"$AWS_LOGGING_BUCKET\",\"Prefix\":\"batch-replication-report\", \"Format\":\"Report_CSV_20180820\",\"Enabled\":true,\"ReportScope\":\"AllTasks\"}" \
  --manifest-generator "{\"S3JobManifestGenerator\": {\"SourceBucket\": \"arn:aws:s3:::$AWS_SOURCE_BUCKET\", \"EnableManifestOutput\": false, \"Filter\": {\"EligibleForReplication\": true, \"ObjectReplicationStatuses\": [\"NONE\",\"FAILED\"]}}}" \
  --priority 1 \
  --role-arn "$AWS_REPLICATION_ROLE" \
  --no-confirmation-required