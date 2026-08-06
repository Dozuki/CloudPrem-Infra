"""
AWS DMS Restart Lambda Function

Purpose: This AWS Lambda function is designed to monitor AWS Data Migration Service (DMS) replications. Upon
detecting an error, it automatically triggers a restart to ensure continuity.

The BI replication is DMS Serverless (a Replication, identified by a replication config ARN). The aurora
migration is still a provisioned ReplicationTask. Both arrive on the same SNS topic, so this handler picks
the right API off the ARN shape rather than assuming one or the other.

Serverless adds a deadline that provisioned tasks do not have: a replication left stopped or failed for 48
hours is deprovisioned and can no longer be resumed at all - recovery becomes a Terraform apply. That is why
restarting promptly matters more here than it did for tasks. There is no separate CloudWatch alarm on the
deprovision event: the alert path is the state-change rule feeding sns_to_slack.py, which pages the channel
on any deprovision wording.

Main Functionality:
1. Extracts the DMS task ARN from the incoming event message.
2. If an error is detected in the task's state change message, the task is restarted.
3. Uses the boto3 library to interface with AWS DMS and initiate task operations.

Functions:
- get_task_arn(message_json): Extracts the DMS task ARN from the message.
- restart_replication(arn): Restarts the specified DMS replication (serverless) or task using 'reload-target'.
- lambda_handler(event, context): Entry point for the Lambda function, processing the event and taking action.

Environment Variables:
- AWS_REGION: Specifies the AWS region for DMS operations.

Usage: This script is intended to be deployed as an AWS Lambda function and triggered by relevant DMS events,
such as task state changes.

Note:
Ensure the AWS Lambda function has the necessary IAM permissions to restart DMS tasks and access relevant resources.
"""

import json
import os
import time
import boto3

# A replication that fails again within this window of an auto-restart is fast-failing
# for a persistent reason (a commercial env 2026-08-03: CPU-bound at the DCU cap - every restart
# re-ran the full load into the same wall, and each reload-target is a fresh full read
# of the production source). First failure still restarts immediately; repeats wait for
# a human. 30 minutes outlives one provision cycle (~15 min) plus enough runtime to
# prove the restart took.
RESTART_COOLDOWN_SECONDS = 1800


def get_task_arn(message_json):
    # Extract the replication (or task) ARN from the message
    return message_json['resources'][0] if message_json['resources'] else None


def recently_restarted(arn):
    # Stateless cooldown: the marker line restart_replication() prints IS the state,
    # queried back out of this function's own log group. No table, no tags (tags on
    # the replication config are terraform-managed and a foreign tag would be mowed
    # on the next apply). Fails open: if the log query itself errors, restart anyway -
    # a missed cooldown is a repeat of today's noise, a false positive is a BI
    # replication sitting failed toward the 48h deprovision deadline.
    log_group = os.environ.get("AWS_LAMBDA_LOG_GROUP_NAME")
    if not log_group:
        return False
    logs_client = boto3.client("logs", region_name=os.environ["AWS_REGION"])
    try:
        resp = logs_client.filter_log_events(
            logGroupName=log_group,
            startTime=int((time.time() - RESTART_COOLDOWN_SECONDS) * 1000),
            filterPattern='"AUTO-RESTART"',
        )
        return any(arn in e.get("message", "") for e in resp.get("events", []))
    except Exception as e:
        print(f"Cooldown check failed ({e}) - failing open and restarting.")
        return False


def restart_replication(arn):
    dms_client = boto3.client("dms", region_name=os.environ["AWS_REGION"])

    if not arn:
        print("No ARN provided.")
        return

    if recently_restarted(arn):
        print(f"AUTO-RESTART suppressed for {arn}: already restarted within "
              f"{RESTART_COOLDOWN_SECONDS}s and failed again - fast-fail loop, a restart "
              f"will not fix it. The CRITICAL state-change alert has already paged.")
        return

    # The marker recently_restarted() searches for. Printed before the start call so a
    # start that itself errors still counts against the cooldown.
    print(f"AUTO-RESTART {arn}")

    # ARN shape is the discriminator: serverless replications are
    # arn:aws:dms:<region>:<acct>:replication-config:<id>, tasks are :task:<id>.
    # reload-target means the same thing on both - reload every table, then resume CDC - so
    # the recovery semantics are unchanged by the move to serverless.
    if ":replication-config:" in arn:
        dms_client.start_replication(
            ReplicationConfigArn=arn,
            StartReplicationType='reload-target'
        )
    else:
        dms_client.start_replication_task(
            ReplicationTaskArn=arn,
            StartReplicationTaskType='reload-target'
        )


def lambda_handler(event, context):
    message_json = json.loads(event['Records'][0]['Sns']['Message'])

    if 'detail-type' in message_json:
        # Process a DMS Replication (serverless) or Replication Task state-change message
        detail_message = message_json['detail'].get('detailMessage', 'N/A')
        task_arn = get_task_arn(message_json)

        # Restart is only safe for the BI replication: reload-target on it rebuilds the
        # writable replica, which is the intended recovery. On any other task
        # (the aurora migration task in particular) reload-target re-runs a full
        # load into a target the migration runner has already staged past the
        # load phase, destroying it. The rule intentionally forwards migration
        # task events for PAGING; this allowlist keeps them alert-only.
        allowed = [a for a in os.environ.get("RESTARTABLE_TASK_ARNS", "").split(",") if a]
        if task_arn not in allowed:
            print(f"Task {task_arn} not in restart allowlist {allowed} - alert-only, no restart.")
            return

        # A deprovisioned serverless replication cannot be restarted at all - AWS reclaims the
        # capacity after 48h stopped/failed and start-replication returns an error. Attempting
        # it anyway would fail this lambda and bury the real problem in a lambda error instead
        # of the state-change alert that already went to Slack. Recovery is a Terraform apply.
        if "deprovision" in detail_message.lower():
            print(f"{task_arn} is deprovisioned - NOT restartable, needs a terraform apply to "
                  f"recreate the replication config. Alert already sent via the state-change rule.")
            return

        # Failure wording differs between the two resource types and neither is a substring of
        # the other. A provisioned ReplicationTask reports messages containing "ERROR"; a
        # serverless Replication reports "DMS replication has failed." with no such token, so
        # matching on "ERROR" alone would silently never restart the BI replication - the
        # failure mode being that everything looks configured and nothing ever fires.
        failed = "ERROR" in detail_message or "has failed" in detail_message.lower()

        if failed:
            restart_replication(task_arn)
