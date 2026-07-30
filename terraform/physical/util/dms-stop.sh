#!/usr/bin/env bash
# Stop the BI DMS replication and wait until it is fully stopped (used before a terraform
# destroy or an endpoint repoint).
#
# Usage: ./dms-stop.sh <DMS_ARN> <AWS_REGION> <AWS_PROFILE>
#
# Takes either shape. The BI replication is DMS Serverless (a replication CONFIG) since the
# serverless release; the pre-serverless stacks and the aurora migration are provisioned
# replication TASKS. describe-replication-tasks does not return a config and
# stop-replication-task cannot stop one, so the ARN picks the API - same dispatch as the
# logical layer's dms-start Job and dms_restart.py.
#
# No longer wired to a destroy-time provisioner, and no longer required for anything. The
# provider stops a replication task before deleting it (v6.56.0
# resourceReplicationTaskDelete -> stopReplicationTask, which waits for stopped), so the
# apply that replaces the old task with a serverless config succeeds on its own. This was a
# workaround for provider issue #2083 and the provider has absorbed it.
#
# Kept as an OPTIONAL operator tool, for two things. Ordering: nothing sequences the old
# task's destroy against the new replication config's create, so pre-stopping avoids a brief
# window where both write the same target. And the rds -> aurora BI switch, which needs the
# replication stopped before DMS will accept ModifyEndpoint on the target.
#
# Note the unconditional sleep 300 on the task path - fine for a deliberate pre-step, which
# is why this is not on any automatic path.

TRIGGER="$1"
AWS_REGION="$2"
AWS_PROFILE="$3"

case "$TRIGGER" in
  *:replication-config:*) KIND=serverless ;;
  *:rep:* | *:replication-task:* | *:task:*) KIND=task ;;
  *)
    echo "unrecognised DMS ARN shape: '$TRIGGER'" >&2
    echo "expected a replication config (serverless) or replication task ARN" >&2
    exit 1
    ;;
esac

function getDMSStatus() {
  if [[ "$KIND" == "serverless" ]]; then
    aws dms describe-replications --filters Name=replication-config-arn,Values="$TRIGGER" --region "$AWS_REGION" --profile "$AWS_PROFILE" --query 'Replications[0].Status' --output text
  else
    aws dms describe-replication-tasks --filter Name=replication-task-arn,Values="$TRIGGER" --without-settings --region "$AWS_REGION" --profile "$AWS_PROFILE" | jq --raw-output '.[][0]["Status"]'
  fi
}

function stopDMS() {
  local STATUS

  STATUS=$(getDMSStatus)
  # A serverless config that has never run has no Replication row at all; --query prints
  # "None". Enumerate what is worth stopping instead of testing for "running" alone, so an
  # unrecognised status is reported rather than silently passed off as stopped.
  case "$STATUS" in
    running | starting | initializing | modifying)
      echo -e "Stopping DMS replication ($STATUS)..."
      if [[ "$KIND" == "serverless" ]]; then
        aws dms stop-replication --replication-config-arn "$TRIGGER" --region "$AWS_REGION" --profile "$AWS_PROFILE" > /dev/null
      else
        aws dms stop-replication-task --replication-task-arn "$TRIGGER" --region "$AWS_REGION" --profile "$AWS_PROFILE" > /dev/null
      fi
      waitforStoppedDMS
      ;;
    stopped | failed | created | ready | None | none)
      echo -e "DMS Already Stopped (status=$STATUS)."
      ;;
    deprovisioning | deprovisioned)
      echo "DMS replication is $STATUS: stopped or failed for 48h, so it cannot be resumed at all." >&2
      echo "Recreate the replication config with a physical apply. Nothing here can fix it." >&2
      exit 1
      ;;
    *)
      echo "unexpected DMS status '$STATUS' - refusing to assume it is stopped" >&2
      exit 1
      ;;
  esac
}

function waitforStoppedDMS() {
  echo "Waiting for the DMS replication to stop..."

  local STATUS WAITED=0

  # Bounded: a stop that never lands should say so rather than hang an operator's pre-step.
  STATUS=$(getDMSStatus)
  while [[ "$STATUS" != "stopped" ]]; do
    if [[ "$WAITED" -ge 900 ]]; then
      echo "DMS replication did not stop in ${WAITED}s (status=$STATUS)" >&2
      exit 1
    fi
    sleep 10
    WAITED=$((WAITED + 10))
    STATUS=$(getDMSStatus)
  done

  # Only the task path needs this. It exists so the task is reported as stopped to the next
  # terraform destroy step; stop-replication on a serverless config is already synchronous
  # enough for the delete, which stops it again anyway.
  if [[ "$KIND" == "task" ]]; then
    sleep 300
  fi

  echo -e "DMS Replication Stopped Successfully."
}

stopDMS
