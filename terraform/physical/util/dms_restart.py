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

        if "ERROR" in detail_message:
            restart_task(task_arn)
