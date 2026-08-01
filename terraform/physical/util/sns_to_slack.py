"""
AWS SNS to Slack Notification Lambda Function

Purpose:
This AWS Lambda function acts as a bridge between AWS SNS notifications and Slack.
It processes incoming SNS notifications related to CloudWatch Alarms and DMS Replication Task State Changes,
formats them, and sends formatted alerts to a designated Slack channel.

Main Functionality:
1. Extracts and formats details from CloudWatch Alarm messages.
2. Extracts and formats details from DMS Replication Task State Change messages.
3. Sends the processed messages to Slack using a predefined webhook URL.

Functions:
- get_account_alias(): Fetches the AWS account alias using the IAM client.
- get_task_name(task_arn): Retrieves the name of a DMS replication task given its ARN.
- lambda_handler(event, context): Entry point for the Lambda function. Processes the event and sends an alert to Slack.

Environment Variables:
- SLACK_BOT_TOKEN_SSM_PARAM: SSM SecureString holding a bot token (chat:write); preferred path.
- SLACK_CHANNEL_ID: Channel the bot-token path posts to.
- SLACK_WEBHOOK_URL: Legacy Slack incoming webhook URL; used when no bot token is configured.
- AWS_REGION: The AWS region where the Lambda function operates.
- AWS_ACCOUNT_ID: The AWS account ID.
- IDENTIFIER: A custom identifier for the AWS setup.

Usage:
This script is intended to be deployed as an AWS Lambda function and triggered by relevant SNS notifications.

Note: Ensure the AWS Lambda function has necessary IAM permissions to fetch account alias, describe replication
tasks, and any other AWS-related operations. Ensure the Slack webhook URL is properly set up and has permissions to
post messages to the designated Slack channel."""

import json
import os
import urllib.request
from datetime import datetime
import boto3

SLACK_WEBHOOK_URL = os.environ.get('SLACK_WEBHOOK_URL', '')
# Bot-token path (preferred): the token is an SSM SecureString read at runtime,
# so it never sits in the function's plaintext env, and chat.postMessage posts
# have the bot as a real author (deletable/editable by token, unlike webhook
# posts). Takes precedence over the webhook when both are configured.
SLACK_BOT_TOKEN_SSM_PARAM = os.environ.get('SLACK_BOT_TOKEN_SSM_PARAM', '')
SLACK_CHANNEL_ID = os.environ.get('SLACK_CHANNEL_ID', '')

# How close a state change must be to the alarm's last config create/update to count
# as alarm settling rather than a real recovery. Wide on purpose: some metrics (DR
# replication, agent-fed ContainerInsights) take hours after an apply to emit their
# first datapoint, and the cost of a too-wide window is one suppressed healthy-state
# post shortly after someone edited that same alarm.
SETTLE_WINDOW_SECONDS = 24 * 3600


def alarm_recently_configured(message_json):
    """True when the state change happened within the settle window of the alarm's
    config being created or updated. Any missing or unparseable timestamp returns
    False so the caller posts the message - never classify on bad data.
    """
    fmt = "%Y-%m-%dT%H:%M:%S.%f%z"
    try:
        state_change = datetime.strptime(message_json["StateChangeTime"], fmt)
        configured = datetime.strptime(
            message_json["AlarmConfigurationUpdatedTimestamp"], fmt)
    except (KeyError, TypeError, ValueError):
        return False
    return abs((state_change - configured).total_seconds()) <= SETTLE_WINDOW_SECONDS


def get_account_alias():
    iam_client = boto3.client('iam')
    aliases = iam_client.list_account_aliases()['AccountAliases']
    return aliases[0] if aliases else None


def get_task_name(task_arn):
    """Resolve a DMS ARN to its human identifier.

    The BI replication is DMS Serverless (a replication-config); the aurora migration is
    still a provisioned task. describe_replication_tasks does not know about a
    replication-config, so calling it with one returns nothing and the old [0] index raised
    IndexError - which killed this lambda BEFORE it posted, meaning DMS failure and
    deprovision alerts silently stopped reaching Slack. Falls back to the ARN's last segment
    rather than raising, because a nameless alert still beats no alert.
    """
    dms_client = boto3.client("dms", region_name=os.environ["AWS_REGION"])

    try:
        if ":replication-config:" in task_arn:
            response = dms_client.describe_replication_configs(
                Filters=[{"Name": "replication-config-arn", "Values": [task_arn]}]
            )
            configs = response.get("ReplicationConfigs") or []
            if configs:
                return configs[0]["ReplicationConfigIdentifier"]
        else:
            response = dms_client.describe_replication_tasks(
                Filters=[{"Name": "replication-task-arn", "Values": [task_arn]}]
            )
            tasks = response.get("ReplicationTasks") or []
            if tasks:
                return tasks[0]["ReplicationTaskIdentifier"]
    except Exception as exc:  # noqa: BLE001 - never let naming break the alert
        print(f"could not resolve DMS name for {task_arn}: {exc}")

    return task_arn.rsplit(":", 1)[-1] or task_arn


def lambda_handler(event, context):
    message_json = json.loads(event['Records'][0]['Sns']['Message'])
    account_id = os.environ["AWS_ACCOUNT_ID"]
    account_alias = get_account_alias() or 'N/A'

    identifier = os.environ["IDENTIFIER"]
    region = os.environ["AWS_REGION"]

    if 'AlarmName' in message_json:
        # Process CloudWatch Alarm message
        alarm_name = message_json.get('AlarmName', 'N/A')
        alarm_description = message_json.get('AlarmDescription', 'N/A')
        old_state_value = message_json.get('OldStateValue')
        new_state_value = message_json.get('NewStateValue', 'N/A')
        new_state_reason = message_json.get('NewStateReason', 'N/A')

        # A freshly created alarm goes INSUFFICIENT_DATA -> OK as its first datapoints
        # arrive. That is alarm birth, not a recovery: an apply that adds a batch of
        # alarms floods the channel with healthy-state posts that read like an incident.
        # The transition alone is not enough to drop, though - an established alarm in
        # ALARM whose metric stops arriving also exits via INSUFFICIENT_DATA -> OK, and
        # that post is the only closure the channel gets. The config-updated timestamp
        # separates the two: only suppress when the alarm was created or edited within
        # the settle window. ALARM -> OK still closes real incidents, anything entering
        # ALARM still pages (missing=breaching alarms arrive via INSUFFICIENT_DATA ->
        # ALARM), and a payload missing OldStateValue or timestamps posts as before.
        if (old_state_value == "INSUFFICIENT_DATA" and new_state_value == "OK"
                and alarm_recently_configured(message_json)):
            print(f"skipping INSUFFICIENT_DATA -> OK for {alarm_name} (alarm settling)")
            return

        if new_state_value == "ALARM":
            header = "*CloudWatch Alarm! <!channel>*"
        else:
            header = "*CloudWatch Notification*"

        slack_message = f"{header}\n\n>Identifier@Region: *{identifier}@{region}*\n>AWS Account ID: {account_id}\n>AWS Account Alias: {account_alias}\n>Alarm: {alarm_name}\n>Description: {alarm_description}\n>State: {new_state_value}\n>Reason: {new_state_reason}"

    elif 'detail-type' in message_json:
        # Process DMS Replication Task State Change message
        detail_type = message_json.get('detail-type', 'N/A')
        detail_message = message_json['detail'].get('detailMessage', 'N/A')
        resource_arn = message_json['resources'][0] if message_json['resources'] else 'N/A'
        replication_task_name = get_task_name(resource_arn)
        # Link to THIS region's console (was hardcoded us-east-1). Serverless replications are
        # not replication tasks and do not exist under #taskDetails, so a config ARN needs the
        # serverless route or the alert links to a page that cannot show the incident.
        if ":replication-config:" in resource_arn:
            replication_task_link = (
                f"https://console.aws.amazon.com/dms/v2/home?region={region}"
                f"#serverlessReplicationDetails/{replication_task_name}"
            )
        else:
            replication_task_link = (
                f"https://console.aws.amazon.com/dms/v2/home?region={region}"
                f"#taskDetails/{replication_task_name}"
            )

        # Page the channel only on failures. A plain "Replication task stopped"
        # is routine (operator stops, the migration's automatic
        # post-full-load stop, fence-time stops) and must not @channel.
        #
        # Deprovision is the exception that carries none of those tokens. A serverless
        # replication stopped or failed for 48h is deprovisioned, which is the one BI state
        # nothing recovers from - the restart lambda declines it and the fix is a terraform
        # apply that recreates the config. Matched on both the detail message and the
        # detail-type, and it catches "deprovisioning" too: that is the transition into the
        # unrecoverable state, not a routine stop, so it is worth the page.
        haystack = f"{detail_message} {detail_type}".lower()
        if ("ERROR" in detail_message or "FATAL" in detail_message
                or "fail" in haystack or "deprovision" in haystack):
            header = "*DMS Alarm! <!channel>*"
        else:
            header = "*DMS Notification*"

        slack_message = f"{header}\n\n>Identifier@Region: *{identifier}@{region}*\n>AWS Account ID: *{account_id}*\n>AWS Account Alias: *{account_alias}*\n>Replication Task: *{replication_task_name}*\n>Detail Type: *{detail_type}*\n>Detail Message: *{detail_message}*\n\nTask Link: <{replication_task_link}>"

    else:
        # Unrecognized message schema
        slack_message = f"Unrecognized message schema: {json.dumps(message_json, indent=2)}"

    if SLACK_BOT_TOKEN_SSM_PARAM and SLACK_CHANNEL_ID:
        token = boto3.client('ssm').get_parameter(
            Name=SLACK_BOT_TOKEN_SSM_PARAM, WithDecryption=True
        )['Parameter']['Value']
        request = urllib.request.Request(
            'https://slack.com/api/chat.postMessage',
            data=json.dumps(
                {'channel': SLACK_CHANNEL_ID, 'text': slack_message}
            ).encode('utf-8'),
            headers={
                'Content-Type': 'application/json; charset=utf-8',
                'Authorization': f'Bearer {token}',
            }
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            body = json.loads(response.read())
        # The Web API reports errors in-body with HTTP 200; raise so the
        # failure is visible in the lambda's logs and retry behavior.
        if not body.get('ok'):
            raise RuntimeError(f"chat.postMessage failed: {body.get('error')}")
    else:
        request = urllib.request.Request(
            SLACK_WEBHOOK_URL,
            data=json.dumps({'text': slack_message}).encode('utf-8'),
            headers={'Content-Type': 'application/json'}
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            response.read()
