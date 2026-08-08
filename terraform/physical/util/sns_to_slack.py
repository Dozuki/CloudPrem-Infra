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
- SLACK_BOT_TOKEN_SSM_PARAM: SSM SecureString holding a bot token (chat:write, reactions:write);
  preferred path.
- SLACK_CHANNEL_ID: Channel the bot-token path posts to.
- SLACK_STATE_TABLE: DynamoDB table used to thread a recovery under the ALARM root that fired.
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
import re
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone
import boto3

# Routine DMS lifecycle chatter, dropped before it reaches Slack.
#
# This list now governs the PROVISIONED path only (the aurora migration task). Serverless
# replications no longer reach it: they are filtered by criticality in lambda_handler,
# because they have a CloudWatch backstop and the migration task does not. See the split
# in the detail-type branch.
#
# Nothing here is a state a human acts on. Failed, stopped, started, running and
# deprovisioned all stay off this list, and the list is a denylist rather than an
# allowlist so a message AWS words differently than we assumed still posts.
#
# Several entries below are serverless vocabulary (the scaling and capacity messages) and
# are unreachable now that serverless is filtered by criticality upstream: a provisioned
# task emits no DCU decisions. They are kept rather than pruned because the cost is zero
# and a denylist that still matches a message AWS decides to reuse is the safer default.
# The scale-up-blocked signal that used to be argued for here is now carried solely by the
# <identifier>-dms-capacity-saturated alarm in monitoring.tf, on an hour of datapoints,
# which is the sustained reading a single event could never give.
#
# What the list still does for the task path is drop 'is initializing', 'is being
# modified' and 'connections tied to'.
#
# Matched only AFTER the failure tokens, because two of these phrases are substrings of
# messages that must page: "provisioning its capacity" sits inside "deprovisioning its
# capacity", and "has been provisioned" inside "has been deprovisioned". Ordering is
# what keeps the 48h-deprovision alert from being swallowed by its own prefix.
DMS_ROUTINE_MESSAGES = (
    'scaling up',
    'scaling down',
    'scaling event completed',
    'cannot scale down',
    'is initializing',
    'preparing the resources for metadata collection',
    # "connections tied to ..." rather than the looser "is being tested", which would also
    # swallow a future message wording a post-failure connection retest.
    'connections tied to',
    'fetching metadata',
    'calculating capacity',
    'provisioning its capacity',
    'has been provisioned',
    'is being modified',
)

SLACK_WEBHOOK_URL = os.environ.get('SLACK_WEBHOOK_URL', '')
# Bot-token path (preferred): the token is an SSM SecureString read at runtime,
# so it never sits in the function's plaintext env, and chat.postMessage posts
# have the bot as a real author (deletable/editable by token, unlike webhook
# posts). Takes precedence over the webhook when both are configured.
SLACK_BOT_TOKEN_SSM_PARAM = os.environ.get('SLACK_BOT_TOKEN_SSM_PARAM', '')
SLACK_CHANNEL_ID = os.environ.get('SLACK_CHANNEL_ID', '')
SLACK_STATE_TABLE = os.environ.get('SLACK_STATE_TABLE', '')

STATE_TTL_SECONDS = 30 * 24 * 3600
COLOR_CRITICAL = '#e01e5a'
COLOR_WARNING = '#ecb22e'
COLOR_RESOLVED = '#2eb67d'
COLOR_INFO = '#36c5f0'

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


def _clip(value, limit=2900):
    text = str(value or 'N/A')
    return text if len(text) <= limit else text[:limit - 1] + '…'


def _parse_time(value):
    try:
        return datetime.fromisoformat(str(value).replace('Z', '+00:00'))
    except (TypeError, ValueError):
        return None


def _slack_relative(value):
    parsed = _parse_time(value)
    if not parsed:
        return _clip(value)
    return f"<!date^{int(parsed.timestamp())}^{{relative}}|{parsed.isoformat()}>"


def _format_duration(started_at, ended_at):
    started = _parse_time(started_at)
    ended = _parse_time(ended_at)
    if not started or not ended:
        return 'Unknown'
    seconds = max(int((ended - started).total_seconds()), 0)
    days, seconds = divmod(seconds, 86400)
    hours, seconds = divmod(seconds, 3600)
    minutes, seconds = divmod(seconds, 60)
    parts = []
    if days:
        parts.append(f'{days}d')
    if hours:
        parts.append(f'{hours}h')
    if minutes:
        parts.append(f'{minutes}m')
    if not parts:
        parts.append(f'{seconds}s')
    return ' '.join(parts[:2])


def _field(label, value):
    return {'type': 'mrkdwn', 'text': f'*{label}*\n{_clip(value, 1900)}'}


def _button(text, url, action_id):
    return {
        'type': 'button',
        'text': {'type': 'plain_text', 'text': text},
        'url': url,
        'action_id': action_id,
    }


def _card(color, header, summary, detail_label, detail, fields,
          evidence_label, evidence, actions, footer, mention=False):
    header_text = f'{header} <!channel>' if mention else header
    blocks = [
        {'type': 'section', 'text': {'type': 'mrkdwn', 'text': f'*{header_text}*'}},
        {'type': 'section', 'text': {'type': 'mrkdwn', 'text': f'*{_clip(summary)}*'}},
    ]
    if detail:
        blocks.append({'type': 'section', 'text': {'type': 'mrkdwn',
                       'text': f'*{detail_label}*\n{_clip(detail)}'}})
    if fields:
        blocks.append({'type': 'section', 'fields': fields})
    if evidence:
        blocks.append({'type': 'section', 'text': {'type': 'mrkdwn',
                       'text': f'*{evidence_label}*\n`{_clip(evidence, 2700)}`'}})
    if actions:
        blocks.append({'type': 'actions', 'elements': actions})
    blocks.append({'type': 'context', 'elements': [
        {'type': 'mrkdwn', 'text': _clip(footer, 1900)}
    ]})
    return {'attachments': [{
        'color': color,
        'fallback': f'{header}: {_clip(summary, 500)}',
        'blocks': blocks,
    }]}


def _alarm_console_url(region, alarm_name):
    encoded = urllib.parse.quote(alarm_name, safe='')
    return (f'https://{region}.console.aws.amazon.com/cloudwatch/home?region={region}'
            f'#alarmsV2:alarm/{encoded}')


def _runbook_url(description):
    match = re.search(r'https?://[^\s<>()]+', description or '')
    return match.group(0).rstrip('.,;)') if match else None


def _alarm_resource(trigger):
    dimensions = trigger.get('Dimensions') or []
    values = []
    for dimension in dimensions:
        if isinstance(dimension, dict):
            value = dimension.get('value') or dimension.get('Value')
            if value:
                values.append(str(value))
    return ', '.join(values) if values else 'Account-wide'


def _alarm_evidence(trigger):
    metric = trigger.get('MetricName') or 'metric'
    statistic = trigger.get('Statistic') or trigger.get('StatisticType') or ''
    operator = {
        'GreaterThanThreshold': '>',
        'GreaterThanOrEqualToThreshold': '≥',
        'LessThanThreshold': '<',
        'LessThanOrEqualToThreshold': '≤',
    }.get(trigger.get('ComparisonOperator'), trigger.get('ComparisonOperator') or '')
    threshold = trigger.get('Threshold', '?')
    evaluations = trigger.get('EvaluationPeriods') or '?'
    datapoints = trigger.get('DatapointsToAlarm') or evaluations
    period = trigger.get('Period')
    window = f'{datapoints}/{evaluations} datapoints'
    if period:
        window += f' × {int(period) // 60 if int(period) >= 60 else int(period)}' + (
            'm' if int(period) >= 60 else 's')
    prefix = f'{statistic} ' if statistic else ''
    return f'{prefix}{metric} {operator} {threshold} · {window}'


def cloudwatch_card(message_json, identifier, region, account_id, account_alias,
                    started_at=None):
    """Return a Slack incident card and state metadata for a CloudWatch event."""
    alarm_name = message_json.get('AlarmName', 'Unknown alarm')
    description = message_json.get('AlarmDescription') or alarm_name
    state = message_json.get('NewStateValue', 'UNKNOWN')
    reason = message_json.get('NewStateReason') or 'CloudWatch did not supply a reason.'
    changed_at = message_json.get('StateChangeTime')
    trigger = message_json.get('Trigger') or {}
    namespace = trigger.get('Namespace') or 'CloudWatch'
    resource = _alarm_resource(trigger)
    console_url = _alarm_console_url(region, alarm_name)
    actions = [_button('Open CloudWatch', console_url, 'open_cloudwatch')]
    runbook = _runbook_url(description)
    if runbook:
        actions.append(_button('Runbook', runbook, 'open_runbook'))

    if state == 'ALARM':
        payload = _card(
            COLOR_CRITICAL,
            f'🔴 CRITICAL · CloudWatch · {identifier}',
            description,
            'IMPACT', reason,
            [_field('SERVICE', namespace), _field('REGION', region),
             _field('RESOURCE', resource),
             _field('STARTED', _slack_relative(started_at or changed_at))],
            'EVIDENCE', _alarm_evidence(trigger), actions,
            f'Active · Investigate now · {alarm_name} · {account_alias} ({account_id})',
            mention=True,
        )
    elif state == 'OK':
        duration = _format_duration(started_at, changed_at) if started_at else 'Unknown'
        payload = _card(
            COLOR_RESOLVED,
            f'✅ RESOLVED · CloudWatch · {identifier}',
            f'{description} is back within its configured threshold.',
            'OUTCOME', reason,
            [_field('SERVICE', namespace), _field('REGION', region),
             _field('DURATION', duration), _field('RESOURCE', resource)],
            'FINAL READING', _alarm_evidence(trigger), actions,
            f'Automatically resolved · {alarm_name} · {account_alias} ({account_id})',
        )
    else:
        payload = _card(
            COLOR_WARNING,
            f'🟠 STATE CHANGE · CloudWatch · {identifier}',
            description,
            'STATUS', reason,
            [_field('SERVICE', namespace), _field('REGION', region),
             _field('STATE', state), _field('RESOURCE', resource)],
            'CONFIGURED SIGNAL', _alarm_evidence(trigger), actions,
            f'Needs review · {alarm_name} · {account_alias} ({account_id})',
        )

    return payload, {
        'alarm_key': message_json.get('AlarmArn') or f'{identifier}:{region}:{alarm_name}',
        'alarm_name': alarm_name,
        'state': state,
        'changed_at': changed_at,
    }


def dms_card(message_json, identifier, region, account_id, account_alias,
             task_name, critical, task_url):
    detail_type = message_json.get('detail-type', 'DMS state change')
    detail = message_json.get('detail') or {}
    detail_message = detail.get('detailMessage', 'DMS did not supply details.')
    header = ('🔴 CRITICAL' if critical else '🔵 UPDATE') + f' · DMS · {identifier}'
    color = COLOR_CRITICAL if critical else COLOR_INFO
    return _card(
        color, header, detail_type,
        'IMPACT' if critical else 'OUTCOME', detail_message,
        [_field('SERVICE', 'AWS DMS'), _field('REGION', region),
         _field('RESOURCE', task_name), _field('ACCOUNT', account_alias)],
        'EVIDENCE', detail_message,
        [_button('Open DMS', task_url, 'open_dms')],
        f'{"Active · Investigate now" if critical else "Operational update"}'
        f' · {account_alias} ({account_id})',
        mention=critical,
    )


def unknown_card(message_json, identifier, region):
    keys = ', '.join(sorted(message_json.keys())) or 'none'
    return _card(
        COLOR_WARNING, f'🟠 UNRECOGNIZED · AWS SNS · {identifier}',
        'A notification used an unsupported schema.',
        'IMPACT', 'The full event is in the Lambda logs for investigation.',
        [_field('REGION', region), _field('TOP-LEVEL KEYS', keys)],
        'EVIDENCE', 'Renderer fallback used', [],
        'Needs renderer review · no raw event data posted to Slack',
    )


def _slack_api(method, token, body):
    request = urllib.request.Request(
        f'https://slack.com/api/{method}',
        data=json.dumps(body).encode('utf-8'),
        headers={
            'Content-Type': 'application/json; charset=utf-8',
            'Authorization': f'Bearer {token}',
        },
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        result = json.loads(response.read())
    if not result.get('ok'):
        raise RuntimeError(f'{method} failed: {result.get("error")}')
    return result


def _post_bot(token, payload, thread_ts=None, reply_broadcast=False):
    body = {'channel': SLACK_CHANNEL_ID, **payload}
    if thread_ts:
        body['thread_ts'] = thread_ts
    if reply_broadcast:
        # Threaded replies are invisible to anyone not already in the thread. A
        # resolution has to land in the channel itself or the incident reads as
        # still open, so the recovery card is broadcast as well as threaded.
        body['reply_broadcast'] = True
    return _slack_api('chat.postMessage', token, body)['ts']


def _update_bot(token, message_ts, payload):
    _slack_api('chat.update', token,
               {'channel': SLACK_CHANNEL_ID, 'ts': message_ts, **payload})


def _add_reaction(token, message_ts, name):
    """Add an emoji reaction to a message. Callers treat this as best effort."""
    _slack_api('reactions.add', token, {
        'channel': SLACK_CHANNEL_ID,
        'timestamp': message_ts,
        'name': name,
    })


def _post_webhook(payload):
    request = urllib.request.Request(
        SLACK_WEBHOOK_URL,
        data=json.dumps(payload).encode('utf-8'),
        headers={'Content-Type': 'application/json'},
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        response.read()


def _bot_token():
    return boto3.client('ssm').get_parameter(
        Name=SLACK_BOT_TOKEN_SSM_PARAM, WithDecryption=True
    )['Parameter']['Value']


def _get_alarm_state(alarm_key):
    if not SLACK_STATE_TABLE:
        return None
    item = boto3.client('dynamodb').get_item(
        TableName=SLACK_STATE_TABLE,
        Key={'AlarmKey': {'S': alarm_key}},
        ConsistentRead=True,
    ).get('Item')
    if not item:
        return None
    return {
        'message_ts': item['MessageTs']['S'],
        'started_at': item['StartedAt']['S'],
        'status': item['Status']['S'],
    }


def _put_alarm_state(alarm_key, message_ts, started_at, status):
    if not SLACK_STATE_TABLE:
        return
    boto3.client('dynamodb').put_item(
        TableName=SLACK_STATE_TABLE,
        Item={
            'AlarmKey': {'S': alarm_key},
            'MessageTs': {'S': message_ts},
            'StartedAt': {'S': started_at or datetime.now(timezone.utc).isoformat()},
            'Status': {'S': status},
            'ExpiresAt': {'N': str(int(time.time()) + STATE_TTL_SECONDS)},
        },
    )


def _deliver_cloudwatch_bot(token, message_json, identifier, region,
                            account_id, account_alias):
    alarm_key = message_json.get('AlarmArn') or (
        f'{identifier}:{region}:{message_json.get("AlarmName", "Unknown alarm")}')
    prior = _get_alarm_state(alarm_key)
    state = message_json.get('NewStateValue', 'UNKNOWN')
    # A newly firing incident may reuse an alarm key whose last row is a resolved
    # idempotency tombstone; only carry its start into a recovery or a duplicate
    # ALARM update, never into the next distinct incident.
    started_at = (prior.get('started_at') if prior and
                  (state != 'ALARM' or prior.get('status') == 'ALARM') else None)
    payload, metadata = cloudwatch_card(
        message_json, identifier, region, account_id, account_alias, started_at)

    if state == 'ALARM':
        if prior and prior.get('status') == 'ALARM':
            # Refreshing an already-red root, not turning it green. This branch mostly
            # fires on duplicate SNS delivery of the same firing event, where an edit
            # is what keeps the channel from showing the same incident twice.
            message_ts = prior['message_ts']
            _update_bot(token, message_ts, payload)
        else:
            message_ts = _post_bot(token, payload)
            started_at = metadata['changed_at'] or datetime.now(timezone.utc).isoformat()
        _put_alarm_state(alarm_key, message_ts, started_at, 'ALARM')
        return

    if state == 'OK' and prior:
        # The ALARM card is written once and never edited on recovery. Editing it
        # green destroyed the only record that the incident happened: an audit of one
        # week found 10 firing records overwritten, 2 of which left no trace at all.
        # The red root stays red; recovery is a new message, not a rewrite.
        if prior.get('status') != 'RESOLVED':
            # Full recovery card, not a one-line summary, so the channel gets the same
            # detail (duration, final reading, resource) it got when the alarm fired.
            # Broadcast because a thread reply alone leaves the channel showing red.
            _post_bot(token, payload, thread_ts=prior['message_ts'],
                      reply_broadcast=True)
            try:
                _add_reaction(token, prior['message_ts'], 'white_check_mark')
            except Exception as exc:  # noqa: BLE001 - a reaction is decoration
                # Never let a reactions failure (scope, already_reacted, Slack 5xx)
                # bubble up: the recovery card is already posted, and raising here
                # would skip the tombstone below and re-post it on the Lambda retry.
                print(f'could not mark {metadata["alarm_name"]} '
                      f'({prior["message_ts"]}) resolved: {exc}')
        _put_alarm_state(alarm_key, prior['message_ts'], prior['started_at'], 'RESOLVED')
        return

    _post_bot(token, payload)


def lambda_handler(event, context):
    message_json = json.loads(event['Records'][0]['Sns']['Message'])
    account_id = os.environ["AWS_ACCOUNT_ID"]
    account_alias = get_account_alias() or 'N/A'
    identifier = os.environ["IDENTIFIER"]
    region = os.environ["AWS_REGION"]

    payload = None
    cloudwatch = False

    if 'AlarmName' in message_json:
        alarm_name = message_json.get('AlarmName', 'N/A')
        old_state_value = message_json.get('OldStateValue')
        new_state_value = message_json.get('NewStateValue', 'N/A')

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
        cloudwatch = True
        payload, _ = cloudwatch_card(
            message_json, identifier, region, account_id, account_alias)

    elif 'detail-type' in message_json:
        detail_type = message_json.get('detail-type', 'N/A')
        detail_message = (message_json.get('detail') or {}).get('detailMessage', 'N/A')
        resources = message_json.get('resources') or []
        resource_arn = resources[0] if resources else 'N/A'

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
        critical = ("ERROR" in detail_message or "FATAL" in detail_message
                    or "fail" in haystack or "deprovision" in haystack)

        # One EventBridge rule feeds two producers, and only one of them has a
        # CloudWatch backstop, so they get different filters.
        #
        # Serverless (a replication-config, the BI replication): post only what is
        # critical. Everything else is scale and lifecycle narration, roughly 225
        # messages a week, and no human acts on any of it. Anything that goes quiet is
        # still caught: the bi_cdc_latency_source/target alarms key on
        # ReplicationConfigId with treat_missing_data = "breaching" at 9 of 12 x 300s,
        # so a stopped, silent or deprovisioned replication alarms on its own inside
        # about 45 minutes. That backstop is why a whitelist by criticality is safe
        # here, and why it replaces growing DMS_ROUTINE_MESSAGES phrase by phrase.
        # Deprovision counts as critical and so still posts immediately, which matters:
        # it is the one BI state nothing recovers from, and the latency alarm would
        # report it 45 minutes later as "latency high or not reporting" instead.
        #
        # Provisioned task (the aurora migration): unchanged, denylist only. It has NO
        # CloudWatch alarm anywhere, so this card is its only alerting and nothing may
        # be dropped that was not already dropped.
        #
        # Both drops happen before the name lookup, not after: get_task_name calls DMS
        # on every event, and the dropped ones are the overwhelming majority. The
        # denylist is still consulted only after the failure tokens - see the substring
        # note on DMS_ROUTINE_MESSAGES.
        # Derived from resources[0], which is 'N/A' when the event carries none. That
        # falls to the provisioned branch and still posts, which is the safe direction now
        # that a wrong answer here costs a dropped card rather than just a wrong console
        # link: the task path drops nothing the denylist did not already drop.
        serverless = ":replication-config:" in resource_arn
        if serverless:
            if not critical:
                print(f"skipping non-critical serverless DMS event: {detail_message}")
                return
        elif not critical and any(p in haystack for p in DMS_ROUTINE_MESSAGES):
            print(f"skipping routine DMS state change: {detail_message}")
            return

        replication_task_name = get_task_name(resource_arn)
        # Link to THIS region's console (was hardcoded us-east-1). Serverless replications are
        # not replication tasks and do not exist under #taskDetails, so a config ARN needs the
        # serverless route or the alert links to a page that cannot show the incident.
        if serverless:
            replication_task_link = (
                f"https://console.aws.amazon.com/dms/v2/home?region={region}"
                f"#serverlessReplicationDetails/{replication_task_name}"
            )
        else:
            replication_task_link = (
                f"https://console.aws.amazon.com/dms/v2/home?region={region}"
                f"#taskDetails/{replication_task_name}"
            )

        payload = dms_card(
            message_json, identifier, region, account_id, account_alias,
            replication_task_name, critical, replication_task_link)

    else:
        # Keep potentially sensitive/raw event data in CloudWatch logs, not Slack.
        print(f"unrecognized SNS message schema: {json.dumps(message_json, default=str)}")
        payload = unknown_card(message_json, identifier, region)

    if SLACK_BOT_TOKEN_SSM_PARAM and SLACK_CHANNEL_ID:
        token = _bot_token()
        if cloudwatch:
            _deliver_cloudwatch_bot(
                token, message_json, identifier, region, account_id, account_alias)
        else:
            _post_bot(token, payload)
    else:
        # Incoming webhooks render the same card, but cannot edit a prior root or
        # create a correlated lifecycle thread. Bot-token installations get that
        # behavior through _deliver_cloudwatch_bot above.
        _post_webhook(payload)
