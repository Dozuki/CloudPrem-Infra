"""
AWS DMS Restart Lambda Function

Purpose: This AWS Lambda function is designed to monitor AWS Data Migration Service (DMS) tasks. Upon detecting an
error in a task, it automatically triggers a restart to ensure continuity.

Main Functionality:
1. Extracts the DMS task ARN from the incoming event message.
2. If an error is detected in the task's state change message, the task is restarted.
3. Uses the boto3 library to interface with AWS DMS and initiate task operations.

Functions:
- get_task_arn(message_json): Extracts the DMS task ARN from the message.
- restart_task(task_arn): Restarts the specified DMS task using the 'reload-target' type.
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
import boto3


def get_task_arn(message_json):
    # Extract the task ARN from the message
    return message_json['resources'][0] if message_json['resources'] else None


def restart_task(task_arn):
    dms_client = boto3.client("dms", region_name=os.environ["AWS_REGION"])

    if task_arn:
        # start the task fresh
        dms_client.start_replication_task(
            ReplicationTaskArn=task_arn,
            StartReplicationTaskType='reload-target'
        )
    else:
        print("No Task ARN provided.")


def lambda_handler(event, context):
    message_json = json.loads(event['Records'][0]['Sns']['Message'])

    if 'detail-type' in message_json:
        # Process DMS Replication Task State Change message
        detail_message = message_json['detail'].get('detailMessage', 'N/A')
        task_arn = get_task_arn(message_json)

        # Restart is only safe for the BI task: reload-target on it rebuilds the
        # writable replica, which is the intended recovery. On any other task
        # (the aurora migration task in particular) reload-target re-runs a full
        # load into a target the migration runner has already staged past the
        # load phase, destroying it. The rule intentionally forwards migration
        # task events for PAGING; this allowlist keeps them alert-only.
        allowed = [a for a in os.environ.get("RESTARTABLE_TASK_ARNS", "").split(",") if a]
        if task_arn not in allowed:
            print(f"Task {task_arn} not in restart allowlist {allowed} - alert-only, no restart.")
            return

        if "ERROR" in detail_message:
            restart_task(task_arn)
