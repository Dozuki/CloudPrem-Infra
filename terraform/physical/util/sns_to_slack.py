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
- SLACK_BOT_TOKEN_SSM_PARAM: SSM SecureString holding a bot token (chat:write,
  reactions:write, and channels:history or groups:history); preferred path. The history
  scope is used only to avoid duplicating a card when a crashed attempt is retried, and
  its absence degrades to the previous at-least-once behaviour rather than failing.
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
import uuid
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

# How long a resolve claim is honoured before another invocation may take it over.
#
# The claim exists so that exactly one delivery of an OK posts the recovery card. The
# lease exists so that a claim whose posting step then died does not silence the
# recovery forever. Sized against the retry cadence rather than the posting time: SNS
# and Lambda async retries are a minute or more apart, so a lease shorter than that
# lets the retry re-claim and post, while a lease longer than that would make the
# retry give up on a card that was never actually sent.
CLAIM_LEASE_SECONDS = 60

# How far back to look in channel history when checking whether a red root was already
# posted by an attempt that died. Only has to cover the gap between a crash and the
# retry that takes the claim over, which is bounded by the lease plus Lambda's retry
# cadence; a wider window would just read more messages for the same answer.
STALE_POST_WINDOW = 15 * 60
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


def _slack_get(method, token, params):
    """Read-side Web API call. conversations.* reads take query params, not a JSON body."""
    request = urllib.request.Request(
        f'https://slack.com/api/{method}?{urllib.parse.urlencode(params)}',
        headers={'Authorization': f'Bearer {token}'},
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        result = json.loads(response.read())
    if not result.get('ok'):
        raise RuntimeError(f'{method} failed: {result.get("error")}')
    return result


def _transition_id(alarm_key, state, changed_at):
    """Identity of ONE alarm transition, stamped into the card's Slack metadata.

    Substring matching on the alarm name or on "RESOLVED" cannot tell this incident's
    card from the last one's: during a fast re-alarm the previous red root and the
    previous green broadcast both still sit in the window and both still contain the
    same words. A takeover could therefore adopt the OLD card as this incident's root
    and suppress the new one. An exact per-transition id has no such ambiguity.
    """
    return f'{alarm_key}|{state}|{changed_at or "-"}'


def _already_posted(token, marker, thread_ts=None, since=None):
    """The ts of an existing message whose metadata carries `marker`, or None.

    Returns the timestamp rather than a bool because the ALARM takeover path needs it:
    finding the root someone else posted is only half the job, the row still has to be
    finalised to point at it or it stays stuck mid-post forever.

    This is what makes a retry idempotent at the Slack boundary. The DynamoDB claim can
    only say "nobody else is posting right now"; it cannot say whether OUR previous
    attempt got its message through before dying, because the failure mode being covered
    is exactly "Slack accepted it and we never learned that". Only Slack knows, so ask
    Slack - but only on a takeover, where a previous attempt actually existed.

    Fails OPEN (returns None, so the caller posts). A missing history scope, a Slack
    5xx, or a thread that cannot be read must not swallow a recovery card: a duplicate
    card is an annoyance, a missing one is the bug this whole change exists to prevent.
    That degradation is why the scope is not a hard dependency.

    Matching is on Slack message METADATA, an exact equality against the transition id,
    and only on bot-authored messages. Both matter: a human typing the alarm name while
    triaging would otherwise suppress the card they were discussing, and a substring
    search cannot distinguish this transition's card from the previous incident's.
    """
    try:
        if thread_ts:
            result = _slack_get('conversations.replies', token, {
                'channel': SLACK_CHANNEL_ID, 'ts': thread_ts, 'limit': 200,
                'include_all_metadata': 'true'})
            # [0] is the root itself, which is not a recovery.
            messages = (result.get('messages') or [])[1:]
        else:
            params = {'channel': SLACK_CHANNEL_ID, 'limit': 200,
                      'include_all_metadata': 'true'}
            if since:
                params['oldest'] = since
            messages = _slack_get('conversations.history', token, params).get(
                'messages') or []
    except Exception as exc:  # noqa: BLE001 - never let a read failure drop a card
        print(f'could not check Slack for an existing "{marker}" post: {exc}')
        return None
    for message in messages:
        payload = (message.get('metadata') or {}).get('event_payload') or {}
        if message.get('bot_id') and payload.get('transition') == marker:
            return message.get('ts')
    return None


def _post_bot(token, payload, thread_ts=None, reply_broadcast=False,
              transition=None):
    body = {'channel': SLACK_CHANNEL_ID, **payload}
    if transition:
        # Carried in metadata rather than the visible text so a retry can recognise its
        # own earlier card exactly, without putting a correlation id in front of humans.
        body['metadata'] = {'event_type': 'cloudwatch_alarm',
                            'event_payload': {'transition': transition}}
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
    # MessageTs and StartedAt do not exist on a row that has been claimed but whose card
    # has not been posted yet. Reading them unconditionally raised KeyError, so every
    # retry of a died-mid-post invocation crashed BEFORE it could take the claim over -
    # the lease could never fire and the card was lost for good.
    return {
        'message_ts': item.get('MessageTs', {}).get('S'),
        'started_at': item.get('StartedAt', {}).get('S'),
        'status': item['Status']['S'],
        'claim_token': item.get('ClaimToken', {}).get('S'),
        'last_event_state': item.get('LastEventState', {}).get('S'),
        # Absent on rows written before this field existed. None means "unknown", which
        # every ordering check below treats as "do not suppress" - an unordered event
        # posts rather than being dropped.
        'last_event_at': (float(item['LastEventAt']['N'])
                          if 'LastEventAt' in item else None),
    }


def _event_epoch(message_json):
    """Epoch seconds for the alarm transition this message describes.

    CloudWatch stamps every state change with StateChangeTime, and that - not arrival
    order - is the only thing that says which of two deliveries happened first. SNS is
    at-least-once and unordered, so without it a delayed OK from a previous incident
    looks exactly like the recovery of the current one.

    Returns None when the field is missing or unparseable. Callers must treat None as
    "cannot order this" and fall through to posting: never classify on bad data.

    Parsing goes through _parse_time (fromisoformat) rather than a strptime format
    string. A literal "%Y-%m-%dT%H:%M:%S.%f%z" requires the fractional second, so a
    timestamp landing exactly on a second boundary would fail to parse and silently opt
    that event out of ordering entirely - the ordering guard would be off for precisely
    the events least likely to be noticed.
    """
    parsed = _parse_time(message_json.get('StateChangeTime'))
    # A naive datetime would be read as process-local time by .timestamp(), which turns
    # a malformed value into a confidently wrong ordering key. CloudWatch always sends an
    # offset, so anything without one is bad data and belongs in the None path.
    if parsed is None or parsed.tzinfo is None:
        return None
    return parsed.timestamp()


def _is_stale(event_at, prior, state=None):
    """True when this delivery must not be applied over what the row already reflects.

    Older than the watermark is stale, plainly.

    Equal to the watermark is stale ONLY when the state differs. An equal timestamp with
    the SAME state is a redelivery of one event and each path handles it (the ALARM path
    edits, the OK path is gated by the claim). An equal timestamp with a DIFFERENT state
    is two conflicting truths about one instant, and nothing here can order them - so
    whichever happens to arrive second would win, and an ALARM could reopen a row that an
    OK resolved at the same second. Refusing both is the only deterministic answer.
    """
    if event_at is None or not prior:
        return False
    last = prior.get('last_event_at')
    if last is None:
        return False
    if event_at < last:
        return True
    last_state = prior.get('last_event_state')
    return event_at == last and last_state is not None and state != last_state


def classify_dms_event(detail_message, detail_type, resource_arn, category=''):
    """Decide what a DMS event does. Returns (outcome, critical).

    outcome is 'DROP', 'POST' or 'PAGE'; critical drives the card's colour and @channel.

    This is a module-level function rather than inline in lambda_handler so the tests can
    call the real decision instead of a hand-copied mirror of it. The mirror was the
    liability: it passed while the handler drifted underneath it.

    One EventBridge rule feeds two producers, and only one has a CloudWatch backstop, so
    they get different filters.

    Serverless (a replication-config, the BI replication): post only what is critical.
    Everything else is scale and lifecycle narration, roughly 225 messages a week, and no
    human acts on any of it. Anything that goes quiet is still caught - the
    bi_cdc_latency_source/target alarms key on ReplicationConfigId with
    treat_missing_data = "breaching" at 9 of 12 x 300s, so a stopped, silent or
    deprovisioned replication alarms on its own inside about 45 minutes. That backstop is
    why a whitelist by criticality is safe here. Deprovision counts as critical and so
    still posts immediately: it is the one BI state nothing recovers from, and the latency
    alarm would report it 45 minutes later as "latency high or not reporting" instead.

    Provisioned task (the aurora migration): denylist only, unchanged. It has NO CloudWatch
    alarm anywhere, so this card is its only alerting and nothing may be dropped that was
    not already dropped.

    A resource_arn of 'N/A' (an event carrying no resources) falls to the provisioned
    branch and still posts, which is the safe direction now that a wrong answer here costs
    a dropped card rather than just a wrong console link.

    Criticality takes AWS's own classification first and prose second. `detail.category` is
    a structured field ("Failure" for the failure category), so it catches a failure worded
    in some way the token list never anticipated - the whole risk of a gate that now
    decides whether a serverless card is sent at all.

    The prose fallback is word-bounded rather than a bare substring. "ERROR"/"FATAL" used to
    be compared against the raw message, so they matched only AWS's uppercase prefixes and a
    message worded "Error:" was dropped in silence. Case-folding fixes that but reintroduces
    the opposite problem: a plain substring test reads "nonfatal" as fatal and "error-free"
    as an error.

    The guards are deliberately narrow, because the two directions are not symmetric: a
    false positive is a noisy card and a false negative is an alert nobody ever sees. So
    only the specific opposites are excluded, not every hyphenated form. A blanket
    [\\w-] guard on both sides looked tidier and silently dropped "task-failed"; a suffix
    group without "s" dropped "fails". Both were regressions against the old substring
    test, which had no false negatives at all.

    What each guard buys:
      (?<!\\w)      "nonfatal" is not fatal
      (?<!non-)     "non-fatal" is not fatal, but "task-failed" still matches
      (?!\\w)       "failover" is not a failure
      (?!-free)     "error-free" is not an error, but "error-code" still matches

    deprovision stays a substring on purpose: it has to match "deprovisioning" (the
    transition into the unrecoverable state) as well as "deprovisioned".
    """
    haystack = f"{detail_message} {detail_type}".casefold()
    critical = (
        str(category).casefold() == 'failure'
        or re.search(
            r'(?<!\w)(?<!non-)(?:error(?:s|ed|ing)?|fatal|fail(?:s|ed|ure|ures|ing)?)(?!\w)(?!-free)',
            haystack) is not None
        or 'deprovision' in haystack
    )

    # The denylist is consulted only after the failure tokens - see the substring note on
    # DMS_ROUTINE_MESSAGES, where two entries are prefixes of messages that must page.
    if ":replication-config:" in resource_arn:
        if not critical:
            return 'DROP', critical
    elif not critical and any(p in haystack for p in DMS_ROUTINE_MESSAGES):
        return 'DROP', critical
    return ('PAGE' if critical else 'POST'), critical


def _pick_dms_arn(resources):
    """Pick the DMS resource ARN out of an event's `resources` list.

    Index 0 was the old rule and is still the fallback. It stopped being good enough once
    the config case started deciding delivery rather than just a console link: a non-DMS
    ARN sitting at index 0 would drop the card outright. Config is checked first because
    it is the case that gates delivery.

    Both task spellings are matched. AWS emits `:task:` (see the ARN note in
    dms_restart.py); `:replication-task:` is the longer form the fixtures carry, and
    `":task:"` is not a substring of it because the character before `task` is a hyphen.
    """
    for marker in (":replication-config:", ":task:", ":replication-task:"):
        for arn in resources:
            if marker in arn:
                return arn
    return resources[0] if resources else 'N/A'


def _claim_resolution(alarm_key, event_at):
    """Atomically take ownership of resolving this incident. True if we own it.

    This is the whole concurrency fix. Two deliveries of the same OK used to both read
    `status == 'ALARM'` and both post; with `reply_broadcast` that is two full green
    cards in the channel, not two thread lines. Now they both attempt this conditional
    transition and DynamoDB picks exactly one winner - the loser returns False and
    posts nothing.

    The condition accepts two starting states:
      * ALARM - the normal case, an incident that is still open.
      * RESOLVING whose claim has outlived CLAIM_LEASE_SECONDS - a previous invocation
        took the claim and died before posting, so the recovery would otherwise be lost.

    and requires the event not be older than the row already reflects, which is what
    stops a delayed OK from a previous incident closing the current one. `<=` rather
    than `<` so a retry of the SAME event can re-claim once its lease expires.

    Returns the claim token (the ClaimedAt we wrote) on success and None otherwise. The
    token is what _finalize_resolution proves ownership with: between claiming and
    finalizing, a new ALARM can legitimately take the row over, and without the token the
    finalize would unconditionally stamp RESOLVED back on top of a live incident.

    Returns None when the table is not configured, so the webhook path is unaffected.
    """
    if not SLACK_STATE_TABLE:
        return None
    now = int(time.time())
    token = uuid.uuid4().hex
    names = {'#s': 'Status'}
    values = {
        ':resolving': {'S': 'RESOLVING'},
        ':token': {'S': token},
        ':alarm': {'S': 'ALARM'},
        ':now': {'N': str(now)},
        ':lease': {'N': str(now - CLAIM_LEASE_SECONDS)},
    }
    condition = ('(#s = :alarm OR (#s = :resolving AND ClaimedAt < :lease))')
    update = 'SET #s = :resolving, ClaimedAt = :now, ClaimToken = :token'
    # Only order when the event carries a usable timestamp. Without one there is nothing
    # to compare, and refusing to claim would drop the recovery entirely.
    if event_at is not None:
        values[':evt'] = {'N': repr(event_at)}
        condition += (' AND (attribute_not_exists(LastEventAt) OR LastEventAt <= :evt)')
        update += ', LastEventAt = :evt'
    client = boto3.client('dynamodb')
    try:
        client.update_item(
            TableName=SLACK_STATE_TABLE,
            Key={'AlarmKey': {'S': alarm_key}},
            UpdateExpression=update,
            ConditionExpression=condition,
            ExpressionAttributeNames=names,
            ExpressionAttributeValues=values,
        )
        return token
    except client.exceptions.ConditionalCheckFailedException:
        # The condition fails for three different reasons and they do NOT share an
        # outcome, so read the row rather than assuming the benign one.
        prior = _get_alarm_state(alarm_key)
        if prior and prior.get('status') in ('RESOLVING', 'ALARM_POSTING'):
            # ALARM_POSTING counts here too. A row mid-root-post cannot satisfy this
            # condition, and returning None would let the caller read that as "duplicate
            # or stale" and drop the recovery outright - permanently, because nothing
            # else revisits it. The root's own invocation (or its retry) finalises the
            # row to ALARM, and this event's retry then claims it normally.
            #
            # Someone holds an unexpired claim. Either they are still posting, or they
            # died between claiming and posting - and nothing here can tell which.
            #
            # Returning False would end this invocation successfully, which tells Lambda
            # the event is handled and drops it from the async queue. If the holder is
            # dead, the recovery card is then gone for good and the lease below never
            # gets a chance to matter: Lambda's first async retry lands at roughly the
            # same minute as the lease, so the retry that was supposed to take over is
            # exactly the one being discarded here.
            #
            # Raising instead lets the retry re-read. By then the holder has either
            # finished (row is RESOLVED, the retry returns False quietly) or its lease
            # has expired (the retry wins the claim and posts). Both converge. Silence
            # only converges when the holder happened to survive.
            raise RuntimeError(
                f'{alarm_key} is claimed by an in-flight post; retrying')
        # RESOLVED means the resolution already happened, and a still-ALARM row means
        # this event is older than the one applied. Both mean "post nothing", and
        # neither is an error. Every other failure - throttling, a missing table,
        # credentials - propagates so the Lambda retries it.
        return None


def _claim_alarm_root(alarm_key, event_at):
    """Take ownership of posting a NEW red root. Returns the claim token, or None.

    The resolve path got a claim first because its duplicate is loud, but the ALARM path
    has the same shape: two deliveries both read "no row" (or the same RESOLVED
    tombstone), both post a root, and only one timestamp survives the write. The other
    root is then permanently untracked - it never gets a check-mark, and its recovery
    threads under the wrong message. That contradicts the "posted once" invariant this
    whole change is built on.

    Accepts a row that does not exist, a RESOLVED tombstone (a genuinely new incident on
    a reused key), a RESOLVING row (a new incident supersedes an in-flight resolve, and
    that resolve's finalize will correctly fail on its token), and an ALARM_POSTING whose
    lease has expired. It does NOT accept a live ALARM - that is the repeat-delivery edit
    path, which is handled without a claim because editing is already idempotent.
    """
    if not SLACK_STATE_TABLE:
        return None
    now = int(time.time())
    token = uuid.uuid4().hex
    values = {
        ':posting': {'S': 'ALARM_POSTING'},
        ':token': {'S': token},
        ':resolved': {'S': 'RESOLVED'},
        ':resolving': {'S': 'RESOLVING'},
        ':now': {'N': str(now)},
        ':lease': {'N': str(now - CLAIM_LEASE_SECONDS)},
    }
    condition = ('(attribute_not_exists(AlarmKey) OR #s = :resolved OR #s = :resolving'
                 ' OR (#s = :posting AND ClaimedAt < :lease))')
    update = 'SET #s = :posting, ClaimedAt = :now, ClaimToken = :token'
    if event_at is not None:
        values[':evt'] = {'N': repr(event_at)}
        condition += ' AND (attribute_not_exists(LastEventAt) OR LastEventAt <= :evt)'
        update += ', LastEventAt = :evt'
    client = boto3.client('dynamodb')
    try:
        client.update_item(
            TableName=SLACK_STATE_TABLE,
            Key={'AlarmKey': {'S': alarm_key}},
            UpdateExpression=update,
            ConditionExpression=condition,
            ExpressionAttributeNames={'#s': 'Status'},
            ExpressionAttributeValues=values,
        )
        return token
    except client.exceptions.ConditionalCheckFailedException:
        prior = _get_alarm_state(alarm_key)
        if prior and prior.get('status') == 'ALARM_POSTING':
            # Another delivery is mid-post. Same reasoning as the resolve claim: going
            # quiet here would consume this event's retry, and if that delivery died the
            # root is lost for good. Raise so the retry re-reads once the lease is up.
            raise RuntimeError(
                f'{alarm_key} root is being posted by another execution; retrying')
        # A live ALARM row means someone finished posting the root already, and a newer
        # LastEventAt means this event is stale. Neither should post.
        return None


def _finalize_alarm_root(alarm_key, claim_token, message_ts, started_at, event_at):
    """Record the root we just posted, if we still own the claim. True if we did."""
    if not SLACK_STATE_TABLE:
        return False
    values = {':posting': {'S': 'ALARM_POSTING'},
              ':claim': {'S': str(claim_token)},
              ':alarm': {'S': 'ALARM'},
              ':ts': {'S': message_ts},
              ':state': {'S': 'ALARM'},
              ':sa': {'S': started_at or datetime.now(timezone.utc).isoformat()}}
    update = ('SET #s = :alarm, MessageTs = :ts, StartedAt = :sa,'
              ' LastEventState = :state REMOVE ExpiresAt')
    if event_at is not None:
        values[':evt'] = {'N': repr(event_at)}
        update = ('SET #s = :alarm, MessageTs = :ts, StartedAt = :sa,'
                  ' LastEventState = :state, LastEventAt = :evt REMOVE ExpiresAt')
    client = boto3.client('dynamodb')
    try:
        client.update_item(
            TableName=SLACK_STATE_TABLE,
            Key={'AlarmKey': {'S': alarm_key}},
            UpdateExpression=update,
            ConditionExpression='#s = :posting AND ClaimToken = :claim',
            ExpressionAttributeNames={'#s': 'Status'},
            ExpressionAttributeValues=values,
        )
        return True
    except client.exceptions.ConditionalCheckFailedException:
        print(f'{alarm_key} was taken over before the root was recorded; '
              f'leaving the newer state alone')
        return False


def _finalize_resolution(alarm_key, claim_token, event_at):
    """Stamp RESOLVED, but only if we still own the claim we took. True if we did.

    Between claiming and posting, a NEW incident can legitimately take the row over: a
    fast flap means ALARM2 arrives, sees a RESOLVING row, posts its own red root and
    writes ALARM. An unconditional write here would then stamp RESOLVED back on top of
    that live incident, and its real recovery would later find a RESOLVED row and post
    nothing - the exact failure this whole change exists to prevent, reintroduced one
    step further along.

    ClaimedAt is the ownership token. If it no longer matches, someone else owns the row
    and the right move is to leave it alone: our green card is already posted, and the
    live incident keeps its own state.
    """
    if not SLACK_STATE_TABLE:
        return False
    values = {':resolving': {'S': 'RESOLVING'},
              ':claim': {'S': str(claim_token)},
              ':resolved': {'S': 'RESOLVED'},
              ':state': {'S': 'OK'},
              ':expires': {'N': str(int(time.time()) + STATE_TTL_SECONDS)}}
    update = 'SET #s = :resolved, ExpiresAt = :expires, LastEventState = :state'
    if event_at is not None:
        values[':evt'] = {'N': repr(event_at)}
        update += ', LastEventAt = :evt'
    client = boto3.client('dynamodb')
    try:
        client.update_item(
            TableName=SLACK_STATE_TABLE,
            Key={'AlarmKey': {'S': alarm_key}},
            UpdateExpression=update,
            ConditionExpression='#s = :resolving AND ClaimToken = :claim',
            ExpressionAttributeNames={'#s': 'Status'},
            ExpressionAttributeValues=values,
        )
        return True
    except client.exceptions.ConditionalCheckFailedException:
        # Superseded mid-resolve. Not an error and not retryable - the card is already
        # in the channel, and forcing a retry would only post it again.
        print(f'{alarm_key} was taken over before the resolve finalised; '
              f'leaving the newer state alone')
        return False


def _touch_alarm_watermark(alarm_key, message_ts, event_at):
    """Advance the watermark on a repeat ALARM. True if the row was still ours to touch.

    This was a whole-item PutItem, which is how a paused duplicate of an OLD incident
    could resurrect it: read the row, pause, the incident resolves, a new incident claims
    and finalises its own root, then the duplicate lands and replaces the whole row with
    the old root and the old event time. The regressed watermark then made a delayed OK
    look newer than the live incident, and it closed it.

    A duplicate needs nothing written except the watermark, so touch only that - and only
    while the row still holds the same root in the same state. Losing that condition is
    not an error: the edit we just made was to a message that is still correct history,
    and whatever superseded us is the state that should stand.
    """
    if not SLACK_STATE_TABLE or event_at is None:
        return False
    client = boto3.client('dynamodb')
    try:
        client.update_item(
            TableName=SLACK_STATE_TABLE,
            Key={'AlarmKey': {'S': alarm_key}},
            UpdateExpression='SET LastEventAt = :evt, LastEventState = :state',
            ConditionExpression=('#s = :alarm AND MessageTs = :ts'
                                 ' AND (attribute_not_exists(LastEventAt)'
                                 ' OR LastEventAt <= :evt)'),
            ExpressionAttributeNames={'#s': 'Status'},
            ExpressionAttributeValues={
                ':alarm': {'S': 'ALARM'},
                ':ts': {'S': message_ts},
                ':state': {'S': 'ALARM'},
                ':evt': {'N': repr(event_at)},
            },
        )
        return True
    except client.exceptions.ConditionalCheckFailedException:
        print(f'{alarm_key} moved on before the duplicate ALARM could touch it')
        return False


def _deliver_cloudwatch_bot(token, message_json, identifier, region,
                            account_id, account_alias):
    alarm_key = message_json.get('AlarmArn') or (
        f'{identifier}:{region}:{message_json.get("AlarmName", "Unknown alarm")}')
    prior = _get_alarm_state(alarm_key)
    state = message_json.get('NewStateValue', 'UNKNOWN')
    event_at = _event_epoch(message_json)

    # SNS is at-least-once and unordered, so a delivery can arrive after the incident it
    # describes has already been superseded. Applying it would rewrite history with an
    # older truth: a stale ALARM reopens an incident that closed, a stale OK closes one
    # that is still burning and then suppresses its real recovery.
    if _is_stale(event_at, prior, state):
        print(f'skipping stale {state} for {alarm_key} '
              f'(event {event_at} older than {prior.get("last_event_at")})')
        return

    # An event with no usable timestamp cannot be placed against the row at all. It used
    # to fall straight through into the claim, which meant a delayed unparseable OK could
    # close a live incident and suppress its real recovery - the very thing ordering was
    # added to stop, walking in through the unordered door. Fail open for VISIBILITY
    # only: post it so nothing is lost, but do not claim, react to, or rewrite the
    # incident row. There is nothing to correlate it to.
    if event_at is None and prior and prior.get('status') in ('ALARM', 'ALARM_POSTING',
                                                              'RESOLVING'):
        print(f'{alarm_key}: {state} carries no usable StateChangeTime; posting '
              f'uncorrelated rather than mutating a live incident')
        payload, _ = cloudwatch_card(
            message_json, identifier, region, account_id, account_alias, None)
        _post_bot(token, payload)
        return

    # A newly firing incident may reuse an alarm key whose last row is a resolved
    # idempotency tombstone; only carry its start into a recovery or a duplicate
    # ALARM update, never into the next distinct incident.
    started_at = (prior.get('started_at') if prior and
                  (state != 'ALARM' or prior.get('status') == 'ALARM') else None)
    payload, metadata = cloudwatch_card(
        message_json, identifier, region, account_id, account_alias, started_at)
    transition = _transition_id(alarm_key, state, metadata['changed_at'])

    if state == 'ALARM':
        if prior and prior.get('status') == 'ALARM':
            # Refreshing an already-red root, not turning it green. This branch mostly
            # fires on duplicate SNS delivery of the same firing event, where an edit
            # is what keeps the channel from showing the same incident twice. No claim
            # needed: editing the same message with the same payload is idempotent.
            message_ts = prior['message_ts']
            _update_bot(token, message_ts, payload)
            _touch_alarm_watermark(alarm_key, message_ts, event_at)
            return

        # A brand new root. Claim before posting for the same reason the resolve does:
        # two deliveries that both see no row would otherwise both post one, and only
        # one timestamp would survive the write.
        claim_token = _claim_alarm_root(alarm_key, event_at)
        # No table means no correlation and therefore no claim to win - but the card
        # still has to go out. Without this guard a bot token configured without a state
        # table drops every alarm, which is worse than the unthreaded posting it replaced.
        if SLACK_STATE_TABLE and not claim_token:
            print(f'skipping duplicate or stale ALARM for {alarm_key}')
            return

        # Only on a takeover, where a previous attempt may have posted before dying.
        # A fresh claim cannot have a root already in the channel.
        existing_ts = None
        if prior and prior.get('status') == 'ALARM_POSTING':
            existing_ts = _already_posted(
                token, transition,
                since=str(int(time.time()) - STALE_POST_WINDOW))

        started_at = metadata['changed_at'] or datetime.now(timezone.utc).isoformat()
        if existing_ts:
            # The attempt that died got its root out before it went. Adopt that message
            # rather than posting a second one - and finalise, or the row sits in
            # ALARM_POSTING with no MessageTs forever and every later OK fails to claim
            # it. Skipping the finalise is how a dedupe turns into a stuck incident.
            print(f'root for {metadata["alarm_name"]} is already in the channel; '
                  f'adopting {existing_ts} instead of posting a second one')
            _finalize_alarm_root(alarm_key, claim_token, existing_ts, started_at,
                                 event_at)
            return

        message_ts = _post_bot(token, payload, transition=transition)
        _finalize_alarm_root(alarm_key, claim_token, message_ts, started_at, event_at)
        return

    if state == 'OK' and prior:
        # The ALARM card is written once and never edited on recovery. Editing it
        # green destroyed the only record that the incident happened: an audit of one
        # week found 10 firing records overwritten, 2 of which left no trace at all.
        # The red root stays red; recovery is a new message, not a rewrite.
        #
        # Claim BEFORE posting, not after. The old order read the status, posted, then
        # wrote the tombstone, so two concurrent deliveries both saw ALARM and both
        # broadcast a full green card into the channel. The claim is a conditional
        # write, so exactly one delivery wins it and the rest return here having sent
        # nothing.
        claim_token = _claim_resolution(alarm_key, event_at)
        if SLACK_STATE_TABLE and not claim_token:
            print(f'skipping duplicate or stale OK for {alarm_key}')
            return

        # A takeover means a previous attempt held this claim and died. It may have got
        # its message to Slack before dying - the claim cannot know, because the failure
        # being covered is "Slack accepted it and we never learned that". Only Slack can
        # answer, and only the thread needs reading. Without this the lease turns every
        # crash into a second green card.
        if (prior.get('status') == 'RESOLVING'
                and _already_posted(token, transition,
                                    thread_ts=prior['message_ts'])):
            print(f'recovery for {metadata["alarm_name"]} is already in the thread; '
                  f'finalising without posting a second one')
            # The attempt that died may have posted the card and gone before reacting,
            # so the root would keep its red-only scrollback forever. reactions.add is
            # idempotent enough for this - already_reacted is caught like any other.
            try:
                _add_reaction(token, prior['message_ts'], 'white_check_mark')
            except Exception as exc:  # noqa: BLE001 - a reaction is decoration
                print(f'could not mark {metadata["alarm_name"]} resolved: {exc}')
            _finalize_resolution(alarm_key, claim_token, event_at)
            return

        # Full recovery card, not a one-line summary, so the channel gets the same
        # detail (duration, final reading, resource) it got when the alarm fired.
        # Broadcast because a thread reply alone leaves the channel showing red.
        #
        # If this raises, the row stays RESOLVING and the Lambda retries. Once the
        # claim's lease expires the retry re-claims, finds the card above, and finalises
        # without duplicating it.
        _post_bot(token, payload, thread_ts=prior['message_ts'],
                  reply_broadcast=True, transition=transition)
        try:
            _add_reaction(token, prior['message_ts'], 'white_check_mark')
        except Exception as exc:  # noqa: BLE001 - a reaction is decoration
            # Never let a reactions failure (scope, already_reacted, Slack 5xx)
            # bubble up: the recovery card is already posted, and raising here
            # would leave the row RESOLVING and re-post it on the Lambda retry.
            print(f'could not mark {metadata["alarm_name"]} '
                  f'({prior["message_ts"]}) resolved: {exc}')
        # Conditional on still owning the claim, not an unconditional write. A fast flap
        # can have handed the row to a new incident while this card was being posted.
        _finalize_resolution(alarm_key, claim_token, event_at)
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
        resource_arn = _pick_dms_arn(resources)

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
        #
        # The decision itself lives in classify_dms_event so the tests can call it rather
        # than reimplement it. Both drops happen before the name lookup, not after:
        # get_task_name calls DMS on every event and the dropped ones are the majority.
        detail_category = (message_json.get('detail') or {}).get('category', '')
        outcome, critical = classify_dms_event(
            detail_message, detail_type, resource_arn, detail_category)
        serverless = ":replication-config:" in resource_arn
        if outcome == 'DROP':
            kind = 'non-critical serverless' if serverless else 'routine'
            print(f"skipping {kind} DMS event: {detail_message}")
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
