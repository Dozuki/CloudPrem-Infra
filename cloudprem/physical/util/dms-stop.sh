#!/usr/bin/env bash

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
