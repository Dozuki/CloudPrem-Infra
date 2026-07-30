#!/usr/bin/env bash
# Stop a DMS replication task and wait until it is fully stopped (used before a
# terraform destroy of the task).
#
# Usage: ./dms-stop.sh <DMS_TASK_ARN> <AWS_REGION> <AWS_PROFILE>
#
# No longer wired to a destroy-time provisioner, and no longer required for anything. The
# provider stops a replication task before deleting it (v6.56.0
# resourceReplicationTaskDelete -> stopReplicationTask, which waits for stopped), so the
# apply that replaces the old task with a serverless config succeeds on its own. This was a
# workaround for provider issue #2083 and the provider has absorbed it.
#
# Kept as an OPTIONAL operator tool. Its remaining use is ordering: nothing sequences the
# old task's destroy against the new replication config's create, so pre-stopping avoids a
# brief window where both write the same target. Note the unconditional sleep 300 below -
# fine for a deliberate pre-step, which is why this is not on any automatic path.

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
