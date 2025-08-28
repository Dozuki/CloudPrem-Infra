#!/usr/bin/env bash
# AWS S3 Batch Replication Script
#
# Purpose:
# This script facilitates the creation of an S3 batch replication job.
# It sets up replication for objects in a source bucket, generating reports for the tasks.
#
# Main Functionality:
# - Accepts several input arguments, including the AWS logging bucket, source bucket, replication role ARN, account ID, partition, and optionally an AWS profile.
# - Configures the AWS CLI command based on the provided profile, if specified.
# - Initiates an S3 batch job to replicate objects, focusing on those that are eligible for replication or have failed replication statuses.
# - Outputs reports regarding the replication tasks to a specified logging bucket.
#
# Usage:
# ./create-s3-batch.sh <AWS_LOGGING_BUCKET> <AWS_SOURCE_BUCKET> <AWS_REPLICATION_ROLE> <AWS_ACCOUNT> <AWS_PARTITION> [AWS_PROFILE]
#
# Required Arguments:
# AWS_LOGGING_BUCKET:  The S3 bucket where replication reports will be stored.
# AWS_SOURCE_BUCKET:   The S3 bucket containing objects to be replicated.
# AWS_REPLICATION_ROLE: The ARN of the role to be used for replication tasks.
# AWS_ACCOUNT:         The AWS account ID.
# AWS_PARTITION:       The AWS partition (e.g., aws, aws-cn, aws-us-gov).
#
# Optional Argument:
# AWS_PROFILE:         The AWS CLI profile to use. If not provided, the default profile or the AWS_PROFILE environment variable is used.
#
# Note:
# Ensure AWS CLI is set up and permissions for S3 and S3 Control are correctly configured for the provided profile.

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