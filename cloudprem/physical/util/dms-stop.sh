#!/usr/bin/env bash

TRIGGER="$1"
AWS_REGION="$2"
AWS_PROFILE="$3"

function checkForStoppedDMS() {
  aws dms describe-replication-tasks --filter Name=replication-task-arn,Values="$TRIGGER" --without-settings --region "$AWS_REGION" --profile "$AWS_PROFILE" |jq --raw-output '.[][0]["Status"]'
}

function waitforStoppedDMS() {
  echo "Waiting for the DMS task to stop..."

   local STATUS

   STATUS=$(checkForStoppedDMS)
   until [[ "$STATUS" == "stopped" ]]; do
      sleep 10
      STATUS=$(checkForStoppedDMS)
   done

   echo -e "DMS Task Stopped Successfully."
}

aws dms stop-replication-task --replication-task-arn "$TRIGGER" --region "$AWS_REGION" --profile "$AWS_PROFILE" > /dev/null

waitforStoppedDMS

