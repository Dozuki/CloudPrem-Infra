import json
import os
import urllib.request
import boto3

SLACK_WEBHOOK_URL = os.environ['SLACK_WEBHOOK_URL']


def get_account_alias():
    iam_client = boto3.client('iam')
    aliases = iam_client.list_account_aliases()['AccountAliases']
    return aliases[0] if aliases else None


def get_task_name(task_arn):
    dms_client = boto3.client("dms", region_name=os.environ["AWS_REGION"])
    response = dms_client.describe_replication_tasks(
        Filters=[{"Name": "replication-task-arn", "Values": [task_arn]}]
    )
    return response["ReplicationTasks"][0]["ReplicationTaskIdentifier"]


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
        new_state_value = message_json.get('NewStateValue', 'N/A')
        new_state_reason = message_json.get('NewStateReason', 'N/A')

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
        replication_task_link = f"https://console.aws.amazon.com/dms/v2/home?region=us-east-1#taskDetails/{replication_task_name}"

        if "stopped" in detail_message or "ERROR" in detail_message:
            header = "*DMS Alarm! <!channel>*"
        else:
            header = "*DMS Notification*"

        slack_message = f"{header}\n\n>Identifier@Region: *{identifier}@{region}*\n>AWS Account ID: *{account_id}*\n>AWS Account Alias: *{account_alias}*\n>Replication Task: *{replication_task_name}*\n>Detail Type: *{detail_type}*\n>Detail Message: *{detail_message}*\n\nTask Link: <{replication_task_link}>"

    else:
        # Unrecognized message schema
        slack_message = f"Unrecognized message schema: {json.dumps(message_json, indent=2)}"

    data = {
        'text': slack_message
    }

    request = urllib.request.Request(
        SLACK_WEBHOOK_URL,
        data=json.dumps(data).encode('utf-8'),
        headers={'Content-Type': 'application/json'}
    )

    with urllib.request.urlopen(request) as response:
        response.read()
