#!/usr/bin/env bash
# AWS Data Migration Service (DMS) Stop Script
#
# Purpose:
# This script facilitates the stopping of a specified AWS DMS replication task.
# It ensures the task transitions to a stopped state before subsequent operations.
#
# Main Functionality:
# - Accepts three input arguments: The DMS task ARN, AWS region, and AWS profile.
# - Fetches the current status of the specified DMS replication task.
# - If the task is running, it issues a stop command and then waits for the task to be fully stopped.
# - Logs messages to inform about the status and progress of the operation.
#
# Usage:
# ./dms-stop.sh <DMS_TASK_ARN> <AWS_REGION> <AWS_PROFILE>
#
# Required Arguments:
# DMS_TASK_ARN:  The ARN of the DMS replication task to be managed.
# AWS_REGION:    The AWS region where the DMS replication task is located.
# AWS_PROFILE:   The AWS CLI profile to be used.
#
# Note:
# Ensure the AWS CLI is properly configured and you have permissions for DMS operations with the provided profile.

TRIGGER="$1"
AWS_REGION="$2"
AWS_PROFILE="$3"

function getDMSStatus() {
  aws dms describe-replication-tasks --filter Name=replication-task-arn,Values="$TRIGGER" --without-settings --region "$AWS_REGION" --profile "$AWS_PROFILE" |jq --raw-output '.[][0]["Status"]'
}

function stopDMS() {
  local STATUS

  STATUS=$(getDMSStatus)
   if [[ "$STATUS" == "running" ]]; then
     echo -e "Stopping DMS Task..."
     aws dms stop-replication-task --replication-task-arn "$TRIGGER" --region "$AWS_REGION" --profile "$AWS_PROFILE" > /dev/null
     waitforStoppedDMS
   else
     echo -e "DMS Already Stopped."
   fi
}

function waitforStoppedDMS() {
  echo "Waiting for the DMS task to stop..."

  local STATUS

  STATUS=$(getDMSStatus)
  while [[ "$STATUS" == "stopping" ]]; do
    sleep 10
    STATUS=$(getDMSStatus)
  done

  # Sleep to make sure the DMS task is reported as stopped to the next terraform destroy step which is destroying the task itself.
  sleep 300

  echo -e "DMS Task Stopped Successfully."
}

stopDMS
