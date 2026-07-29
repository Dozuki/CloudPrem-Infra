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

# Export rather than building an "AWS_PROFILE=x" command prefix. The shell recognises
# assignment prefixes BEFORE parameter expansion, so a variable that expands to
# "AWS_PROFILE=default" is treated as a command name, not an assignment:
#   ./create-s3-batch.sh: line 30: AWS_PROFILE=default: command not found  (exit 127)
# Spacelift never passes a profile, so this only bit runs that do - the DR rebuild and
# the S3 migration, i.e. exactly the paths where the pre-existing objects are the whole
# point. Found by the first recovery-rebuild drill.
if [ "${6:-}" != "" ]; then
  export AWS_PROFILE="$6"
fi

# IAM propagation slack. This is NOT what orders the job after its permissions: the job is
# authorised by the replication POLICY, and this sleep was calibrated against the role, which
# Terraform creates earlier. On the sharedgov build the role existed 40s before the job and the
# policy appeared 8s AFTER it, so every job failed AccessDenied on the manifest with 0 tasks run.
# The ordering is enforced by the depends_on in s3.tf; keep both.
sleep 30

aws s3control create-job \
  --account-id "$AWS_ACCOUNT" \
  --operation '{"S3ReplicateObject":{}}' \
  --description "Source $AWS_SOURCE_BUCKET" \
  --report "{\"Bucket\":\"$AWS_LOGGING_BUCKET\",\"Prefix\":\"batch-replication-report\", \"Format\":\"Report_CSV_20180820\",\"Enabled\":true,\"ReportScope\":\"AllTasks\"}" \
  --manifest-generator "{\"S3JobManifestGenerator\": {\"SourceBucket\": \"arn:$AWS_PARTITION:s3:::$AWS_SOURCE_BUCKET\", \"EnableManifestOutput\": false, \"Filter\": {\"EligibleForReplication\": true}}}" \
  --priority 1 \
  --role-arn "$AWS_REPLICATION_ROLE" \
  --no-confirmation-required