"""
Fixture matrix for the DMS routing and the CloudWatch alarm lifecycle in sns_to_slack.py.

Run: python3 util/test_sns_to_slack.py   (no pytest; needs boto3 importable, exits non-zero
on failure)

Two DMS producers share one EventBridge rule and one lambda, and they are filtered
differently, so the fixtures come in two sets:

  * PROVISIONED (the aurora migration task, a replication-task ARN). Denylist only. This
    task has no CloudWatch alarm anywhere, so the card is its only alerting and a
    regression that drops one of these is silent data loss.
  * SERVERLESS (the BI replication, a replication-config ARN). Critical only. The CDC
    latency alarms are the backstop for anything that goes quiet.

The denylist half exists because the routing is substring matching against AWS's own event
wording, and two of the phrases in DMS_ROUTINE_MESSAGES are prefixes of messages that MUST
page:

    "provisioning its capacity"  is inside  "deprovisioning its capacity"
    "has been provisioned"       is inside  "has been deprovisioned"

Only the order of the checks keeps those alerts alive - the failure tokens are evaluated
before the denylist is consulted. That is invisible at the call site and trivially broken by
someone "tidying up" the branch, hence a test rather than a comment.

Messages below are AWS's documented wording for both source types (Replication and
ReplicationTask, from the DMS user guide's EventBridge event tables) plus the scale-block
variants observed live, which are not in the published tables.

The lifecycle half pins the resolve semantics: a red ALARM root is posted once and never
edited, because editing it green erased the record that the incident happened at all.

The module is loaded by path rather than imported so this file does not depend on the
package layout of whatever directory terraform zips it into.
"""

import json
import os
import sys
import time
import importlib.util
from unittest import mock

_HERE = os.path.dirname(os.path.abspath(__file__))

# Import sns_to_slack without executing its boto3-dependent handler. The module reads env
# vars at import; none are required to have values, so a bare import is safe.
_spec = importlib.util.spec_from_file_location(
    "sns_to_slack", os.path.join(_HERE, "sns_to_slack.py")
)
sns_to_slack = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sns_to_slack)

DROP = sns_to_slack.DMS_ROUTINE_MESSAGES


class _ConditionalCheckFailedException(Exception):
    """Stand-in for botocore's dynamically generated error class.

    The real one only exists on a live client's `exceptions` namespace, which is exactly
    how _claim_resolution catches it. The fake client below exposes this under the same
    attribute so the production code path is the one under test, rather than a
    name-matching shortcut written for the suite's benefit.
    """


class _FakeExceptions:
    ConditionalCheckFailedException = _ConditionalCheckFailedException


# The aurora migration task and the BI serverless replication, which are the two producers
# behind the single EventBridge rule that feeds this lambda.
TASK_ARN = "arn:aws:dms:us-east-1:111:replication-task:EXAMPLETASKID"
SERVERLESS_ARN = "arn:aws:dms:us-east-1:111:replication-config:EXAMPLECONFIGID"


def classify(detail_message, detail_type="DMS Replication State Change",
             resource_arn=TASK_ARN, category=""):
    """Thin adapter over the REAL classifier in sns_to_slack.py.

    This used to be a hand-copied mirror of lambda_handler's routing, which is a trap: the
    mirror kept passing while the handler drifted underneath it, so the fixtures below
    proved only that the copy agreed with itself. Now the fixtures drive production code
    and the copy cannot rot.
    """
    outcome, _ = sns_to_slack.classify_dms_event(
        detail_message, detail_type, resource_arn, category)
    return outcome


# (message, expected outcome, why it matters)
#
# PROVISIONED path (replication-task ARN). This is the aurora migration task, which has no
# CloudWatch alarm of any kind, so nothing here may start dropping. Every expectation below
# is the behaviour that shipped before the serverless split and must survive it unchanged.
CASES = [
    # --- the reported noise, and every scaling decision ---
    ("The replication, 'acme-bi', cannot scale down as the replication is already at the "
     "provided Minimum DMS Capacity Units, '2'.", "DROP", "the alert that started this"),
    ("DMS replication scaling up event.", "DROP", "routine autoscaling"),
    ("DMS replication scaling down event.", "DROP", "routine autoscaling"),
    ("DMS replication scaling event completed.", "DROP", "routine autoscaling"),

    # --- scale-blocked-at-MAX is deliberately NOT dropped: opposite meaning ---
    ("The replication, 'acme-bi', cannot scale up as the replication is already at the "
     "provided Maximum DMS Capacity Units, '32'.", "POST",
     "ceiling is binding - the strongest point-in-time under-provisioning signal"),

    # --- provisioning pipeline chatter ---
    ("DMS replication is initializing.", "DROP", "lifecycle step"),
    ("DMS replication is preparing the resources for metadata collection.", "DROP", "lifecycle step"),
    ("The connections tied to DMS replication is being tested.", "DROP", "lifecycle step"),
    ("DMS replication is fetching metadata", "DROP", "lifecycle step"),
    ("DMS replication is calculating capacity", "DROP", "lifecycle step"),
    ("DMS replication is provisioning its capacity", "DROP", "lifecycle step"),
    ("DMS replication has been provisioned.", "DROP", "lifecycle step"),
    ("DMS replication is being modified.", "DROP", "emitted by our own DCU/compute applies"),

    # --- THE SUBSTRING TRAPS: these must survive the phrases that prefix them ---
    ("DMS replication is deprovisioning its capacity", "PAGE",
     "prefixed by 'provisioning its capacity' - unrecoverable state, must not be dropped"),
    ("DMS replication has been deprovisioned.", "PAGE",
     "prefixed by 'has been provisioned' - unrecoverable state, must not be dropped"),

    # --- failures: serverless wording carries no ERROR token ---
    ("DMS replication has failed.", "PAGE", "serverless failure, no ERROR token"),
    ("A replication task has failed.", "PAGE", "task failure"),
    ("A call to clean task data has failed.", "PAGE", "task failure"),
    ("Creation of target task failed.", "PAGE", "task failure"),
    ("Replication task assessment run has finished with failure.", "PAGE", "task failure"),
    ("Last Error  Task error notification received from subtask 0, thread 0 "
     "[reptask/replicationtask.c:2891] [1020101] ERROR: out of memory", "PAGE",
     "the OOM under-provisioning kills us with"),

    # --- failure carrying a denylisted phrase: ordering must win ---
    ("DMS replication scaling up event failed.", "PAGE", "failure token beats denylist"),
    ("DMS replication is provisioning its capacity - ERROR: out of memory", "PAGE",
     "failure token beats denylist"),
    ("DMS replication has been provisioned but has failed.", "PAGE",
     "failure token beats denylist"),

    # --- stops and starts: quieter than a page, but must still reach slack ---
    ("DMS replication has stopped", "POST", "48h deprovision clock starts here"),
    ("DMS replication is being stopped.", "POST", "operator or apply-driven stop"),
    ("DMS replication has started", "POST", "restart confirmation"),
    ("DMS replication is running.", "POST", "restart confirmation"),
    ("DMS replication has been created.", "POST", "lifecycle"),
    ("DMS replication is being deleted.", "POST", "lifecycle"),

    # --- aurora migration ReplicationTask events share this rule and lambda ---
    ("Replication task stopped", "POST",
     "DMS-EVENT-0079 - a soak-phase stop sitting unnoticed past binlog retention kills "
     "the migration"),
    ("The replication task has started with taskType: full-load-and-cdc, "
     "startType: resume-processing", "POST", "migration task start"),
    ("Reading paused, swap files limit reached.", "POST", "DMS-EVENT-0091"),
    ("Reading paused, disk usage limit reached.", "POST", "DMS-EVENT-0092"),
    ("Reading resumed.", "POST", "DMS-EVENT-0093"),
    ("Reload of table details has been requested.", "POST", "DMS-EVENT-0081"),
    ("The replication task has been deleted.", "POST", "DMS-EVENT-0073"),
    ("A replication task has been modified.", "POST",
     "'has been modified' must NOT match the 'is being modified' denylist entry"),
]

# SERVERLESS path (replication-config ARN). Critical posts, everything else is dropped -
# including states the provisioned path still posts, because the bi_cdc_latency_source and
# bi_cdc_latency_target alarms go ALARM on their own within about 45 minutes if the
# replication stops reporting. Deprovision is the state that must not wait for them.
SERVERLESS_CASES = [
    # --- the routine churn this gate exists to delete, roughly 225 messages a week ---
    ("The replication, 'bi-replication', cannot scale down as the replication is already "
     "at the provided Minimum DMS Capacity Units, '2'.", "DROP", "scale-cycle churn"),
    ("DMS replication scaling up event.", "DROP", "scale-cycle churn"),
    ("DMS replication is initializing.", "DROP", "provisioning pipeline step"),
    ("DMS replication has been provisioned.", "DROP", "provisioning pipeline step"),
    ("DMS replication is being modified.", "DROP", "emitted by our own DCU applies"),

    # --- states the provisioned path still posts, now covered by the CDC latency alarms ---
    ("DMS replication has stopped", "DROP", "alarm goes breaching within ~45min"),
    ("DMS replication has started", "DROP", "no human acts on a restart confirmation"),
    ("DMS replication is running.", "DROP", "no human acts on a healthy state"),
    ("DMS replication has been created.", "DROP", "lifecycle, alarm covers the outcome"),
    ("The replication, 'bi-replication', cannot scale up as the replication is already at "
     "the provided Maximum DMS Capacity Units, '32'.", "DROP",
     "capacity pressure is the -dms-capacity-saturated alarm's job, not a card"),

    # --- one case per critical token, which is the whole whitelist ---
    ("Last Error  Task error notification received from subtask 0, thread 0 "
     "[reptask/replicationtask.c:2891] [1020101] ERROR: out of memory", "PAGE",
     "ERROR token"),
    ("FATAL: the replication instance ran out of storage.", "PAGE",
     "FATAL token, carries no other failure word"),
    ("DMS replication has failed.", "PAGE", "fail token, serverless carries no ERROR"),
    ("DMS replication has been deprovisioned.", "PAGE",
     "deprovision token - the one BI state nothing recovers from"),

    # --- THE SUBSTRING TRAPS survive the new gate too ---
    ("DMS replication is deprovisioning its capacity", "PAGE",
     "prefixed by 'provisioning its capacity' - the transition into the unrecoverable "
     "state, and the alarm only reports it ~45min later as 'not reporting'"),
]


def alarm_fixture(state="ALARM"):
    return {
        "AlarmName": "acme-prod-api-5xx",
        "AlarmDescription": "Checkout API error rate is elevated. https://runbooks.example/api-5xx",
        "AlarmArn": "arn:aws:cloudwatch:us-east-1:111:alarm:acme-prod-api-5xx",
        "OldStateValue": "OK" if state == "ALARM" else "ALARM",
        "NewStateValue": state,
        "NewStateReason": ("Threshold Crossed: 8 datapoints were above 5"
                           if state == "ALARM" else "Threshold Crossed: 3 datapoints were below 5"),
        "StateChangeTime": ("2026-08-01T12:00:00.000+0000"
                            if state == "ALARM" else "2026-08-01T12:08:00.000+0000"),
        "Trigger": {
            "Namespace": "AWS/ApplicationELB",
            "MetricName": "HTTPCode_Target_5XX_Count",
            "Statistic": "Sum",
            "ComparisonOperator": "GreaterThanThreshold",
            "Threshold": 5,
            "EvaluationPeriods": 3,
            "DatapointsToAlarm": 3,
            "Period": 60,
            "Dimensions": [{"name": "LoadBalancer", "value": "app/checkout/abc"}],
        },
    }


def run_dms_event(detail_message, resource_arn,
                  detail_type="DMS Replication State Change", category=None):
    """Drive the real lambda_handler over a DMS event on the webhook path.

    The fixture matrix above tests a mirror of the routing; this runs the handler itself,
    so the two disagree loudly if the mirror drifts. Returns the payloads that reached
    Slack and the ARNs get_task_name was called with - the second list is the assertion
    that a drop happens BEFORE the DMS lookup, which costs an API call on every event.

    resource_arn takes a single ARN, a list of them for a multi-resource event, or None to
    omit the 'resources' key the way an event that carries none does.

    category populates AWS's structured detail.category when given.
    """
    message = {
        'detail-type': detail_type,
        'detail': {'detailMessage': detail_message},
    }
    if category is not None:
        message['detail']['category'] = category
    if resource_arn is not None:
        message['resources'] = (list(resource_arn) if isinstance(resource_arn, list)
                                else [resource_arn])
    event = {'Records': [{'Sns': {'Message': json.dumps(message)}}]}
    posted, lookups = [], []
    with mock.patch.dict(os.environ, {'AWS_ACCOUNT_ID': '111', 'IDENTIFIER': 'test-env',
                                      'AWS_REGION': 'us-east-1'}), \
         mock.patch.object(sns_to_slack, 'SLACK_BOT_TOKEN_SSM_PARAM', ''), \
         mock.patch.object(sns_to_slack, 'SLACK_CHANNEL_ID', ''), \
         mock.patch.object(sns_to_slack, 'get_account_alias', return_value='prod'), \
         mock.patch.object(sns_to_slack, 'get_task_name',
                           side_effect=lambda arn: lookups.append(arn) or 'replication'), \
         mock.patch.object(sns_to_slack, '_post_webhook', side_effect=posted.append):
        sns_to_slack.lambda_handler(event, None)
    return posted, lookups


def deliver_cloudwatch(state, prior, reaction_error=None, claim=True,
                       changed_at=None):
    """Run _deliver_cloudwatch_bot with every Slack and DynamoDB call captured.

    claim stands in for the conditional write's verdict: True means this delivery won
    the ALARM -> RESOLVING transition, False means DynamoDB rejected it because another
    delivery already owns the resolve or the event is older than the row. The condition
    itself is DynamoDB's to evaluate - what these checks pin is that we ask before
    posting and honour the answer. _claim_resolution's request shape is asserted
    separately in claim_checks().
    """
    updates, posts, states, reactions, claims, finals = [], [], [], [], [], []

    def _post(token, payload, thread_ts=None, reply_broadcast=False):
        posts.append({'payload': payload, 'thread_ts': thread_ts,
                      'broadcast': reply_broadcast})
        return '333.444'

    def _react(token, message_ts, name):
        reactions.append((message_ts, name))
        if reaction_error:
            raise RuntimeError(reaction_error)

    def _claim(alarm_key, event_at):
        claims.append((alarm_key, event_at))
        return 1754049600 if claim else None

    def _finalize(alarm_key, claim_token, event_at):
        finals.append((alarm_key, claim_token, event_at))
        return True

    fixture = alarm_fixture(state)
    if changed_at is not None:
        fixture['StateChangeTime'] = changed_at

    with mock.patch.object(sns_to_slack, '_get_alarm_state', return_value=prior), \
         mock.patch.object(sns_to_slack, '_update_bot',
                           side_effect=lambda token, ts, payload: updates.append((ts, payload))), \
         mock.patch.object(sns_to_slack, '_post_bot', side_effect=_post), \
         mock.patch.object(sns_to_slack, '_add_reaction', side_effect=_react), \
         mock.patch.object(sns_to_slack, '_claim_resolution', side_effect=_claim), \
         mock.patch.object(sns_to_slack, '_finalize_resolution', side_effect=_finalize), \
         mock.patch.object(sns_to_slack, '_put_alarm_state',
                           side_effect=lambda *args: states.append(args)):
        sns_to_slack._deliver_cloudwatch_bot(
            'xoxb', fixture, 'acme-prod', 'us-east-1', '111', 'prod')
    return updates, posts, states, reactions, claims, finals


def slack_body_checks():
    """What the Slack wrappers actually put on the wire.

    The lifecycle checks below patch _post_bot and _add_reaction, so they prove the call
    was made with the right arguments and nothing about the request body. A broadcast that
    never reaches chat.postMessage looks identical to them, and looks identical in the
    channel to no fix at all.
    """
    calls = []
    with mock.patch.object(sns_to_slack, 'SLACK_CHANNEL_ID', 'C123'), \
         mock.patch.object(sns_to_slack, '_slack_api',
                           side_effect=lambda method, token, body:
                           calls.append((method, body)) or {'ts': '333.444'}):
        sns_to_slack._post_bot('xoxb', {'text': 'root'})
        sns_to_slack._post_bot('xoxb', {'text': 'recovery'}, thread_ts='111.222',
                               reply_broadcast=True)
        sns_to_slack._add_reaction('xoxb', '111.222', 'white_check_mark')

    root, reply, reaction = calls[0][1], calls[1][1], calls[2][1]
    return [
        ("root post carries no thread or broadcast",
         calls[0][0] == 'chat.postMessage' and 'thread_ts' not in root
         and 'reply_broadcast' not in root),
        ("recovery post carries thread_ts on the wire", reply.get('thread_ts') == '111.222'),
        ("recovery post carries reply_broadcast on the wire",
         reply.get('reply_broadcast') is True),
        ("reaction calls reactions.add on the root",
         calls[2][0] == 'reactions.add' and reaction.get('timestamp') == '111.222'
         and reaction.get('name') == 'white_check_mark'
         and reaction.get('channel') == 'C123'),
    ]


def dms_routing_checks():
    """End-to-end checks on the serverless-vs-provisioned split in lambda_handler."""
    checks = []

    # Deliberately a message the denylist does NOT contain, and which the provisioned path
    # still posts. If the criticality gate is removed, the denylist cannot cover for it.
    posted, lookups = run_dms_event('DMS replication has stopped', SERVERLESS_ARN)
    checks.extend([
        ("serverless non-critical is dropped", posted == []),
        ("serverless drop skips the DMS name lookup", lookups == []),
    ])

    posted, _ = run_dms_event('DMS replication has stopped', TASK_ARN)
    checks.append(("the same message on the provisioned path still posts", len(posted) == 1))

    posted, lookups = run_dms_event(
        "The replication, 'bi-replication', cannot scale down as the replication is "
        "already at the provided Minimum DMS Capacity Units, '2'.", SERVERLESS_ARN)
    checks.extend([
        ("serverless scale churn is dropped", posted == []),
        ("serverless scale churn skips the DMS name lookup", lookups == []),
    ])

    # One check per token in the critical test, because the whitelist is now the only
    # thing keeping a serverless event alive. The lowercase pair is deliberate: the gate
    # used to compare these two against the raw message, so a message worded "Error:"
    # rather than "ERROR:" was dropped in silence.
    for token, message in (
        ('ERROR', 'Task error notification received [1020101] ERROR: out of memory'),
        ('FATAL', 'FATAL: the replication instance ran out of storage.'),
        ('lowercase error', 'Error: the replication could not reach the source endpoint.'),
        ('lowercase fatal', 'Fatal exception in the replication engine.'),
        ('fail', 'DMS replication has failed.'),
        ('deprovision', 'DMS replication has been deprovisioned.'),
    ):
        posted, _ = run_dms_event(message, SERVERLESS_ARN)
        checks.append((f'serverless {token} still posts',
                       len(posted) == 1 and '<!channel>' in str(posted[0])))

    # The substring collisions the denylist ordering used to protect. The serverless gate
    # must not reintroduce them from the other direction.
    for message in ('DMS replication is deprovisioning its capacity',
                    'DMS replication has been deprovisioned.'):
        for arn, path in ((SERVERLESS_ARN, 'serverless'), (TASK_ARN, 'provisioned')):
            posted, _ = run_dms_event(message, arn)
            checks.append((f'{path} pages on "{message[-28:]}"',
                           len(posted) == 1 and '<!channel>' in str(posted[0])))

    # The no-regression case that matters most: the migration task has no alarm behind it.
    posted, lookups = run_dms_event('Replication task stopped', TASK_ARN,
                                    detail_type='DMS Replication Task State Change')
    checks.extend([
        ("provisioned non-critical still posts", len(posted) == 1),
        ("provisioned non-critical does not page", '<!channel>' not in str(posted[0] if posted else '')),
        ("provisioned post resolves its name", lookups == [TASK_ARN]),
    ])

    posted, lookups = run_dms_event('DMS replication scaling up event.', TASK_ARN)
    checks.extend([
        ("provisioned denylist still drops", posted == []),
        ("provisioned drop skips the DMS name lookup", lookups == []),
    ])

    # The two shapes the ARN lookup promises to survive. Neither has ever been observed:
    # AWS sends one resource per event today. They are pinned because the flag they feed
    # now decides delivery, so a wrong answer costs a card rather than a console link.
    posted, _ = run_dms_event('DMS replication has stopped', None)
    checks.append(("an event carrying no resources falls to the provisioned path and posts",
                   len(posted) == 1))

    multi = ["arn:aws:sns:us-east-1:111:some-topic", SERVERLESS_ARN]
    posted, lookups = run_dms_event('DMS replication has stopped', multi)
    checks.extend([
        ("a config ARN past index 0 is still read as serverless", posted == []),
        ("that drop still skips the DMS name lookup", lookups == []),
    ])

    posted, _ = run_dms_event('DMS replication has failed.', multi)
    checks.append(("a critical multi-resource event links to the serverless console",
                   len(posted) == 1 and 'serverlessReplicationDetails' in str(posted[0])))

    # The provisioned half of the same lookup. Routing is safe either way here (a topic ARN
    # carries no ':replication-config:' so it lands on the task path regardless), but the
    # ARN also feeds get_task_name and the console URL, so picking index 0 would resolve a
    # name from the topic and deep-link to nothing.
    multi_task = ["arn:aws:sns:us-east-1:111:some-topic", TASK_ARN]
    posted, lookups = run_dms_event('Replication task stopped', multi_task,
                                    detail_type='DMS Replication Task State Change')
    checks.extend([
        ("a task ARN past index 0 still posts", len(posted) == 1),
        ("that post resolves its name from the task, not the topic", lookups == [TASK_ARN]),
        ("that post links to the provisioned console",
         posted and 'taskDetails' in str(posted[0])),
    ])

    # The shape AWS actually emits. The fixture ARN above spells it :replication-task:,
    # but dms_restart.py's ARN note records the real one as :task:<id>, and ":task:" is
    # not a substring of ":replication-task:" - the character before `task` is a hyphen.
    # So a lookup that only knew the long form would miss every real multi-resource task
    # event, which is exactly the case the lookup exists for.
    real_task_arn = "arn:aws:dms:us-east-1:111:task:EXAMPLETASKID"
    posted, lookups = run_dms_event(
        'Replication task stopped', ["arn:aws:sns:us-east-1:111:some-topic", real_task_arn],
        detail_type='DMS Replication Task State Change')
    checks.extend([
        ("a real :task: ARN past index 0 still posts", len(posted) == 1),
        ("that post resolves its name from the :task: ARN", lookups == [real_task_arn]),
    ])

    # AWS's structured classification, which catches a failure worded in a way the prose
    # list never anticipated. This is the backstop for the whole criticality gate.
    posted, _ = run_dms_event('Something nobody wrote a token for.', SERVERLESS_ARN,
                              category='Failure')
    checks.append(("serverless detail.category=Failure posts on novel wording",
                   len(posted) == 1))

    # The criticality regex, both directions. False positives are a noisy card; false
    # negatives are an alert nobody ever sees, so the MUST-PAGE half matters more. Every
    # entry below is a wording the plain-substring test used to catch, or a collision the
    # word-bounded one has to keep rejecting - the two regressions that got caught in
    # review were "fails" (suffix group had no s) and "task-failed" (blanket hyphen ban).
    for message in ('The replication task fails to start.',
                    'Event: task-failed on the source endpoint.',
                    'Replication failures exceeded the retry budget.',
                    'The task is failing repeatedly.',
                    'The replication stopped some tables due to errors.',
                    'The task errored out during the full load.',
                    'A fatal condition stopped the replication.',
                    'error-code 1020101 encountered during CDC.'):
        posted, _ = run_dms_event(message, SERVERLESS_ARN)
        checks.append((f'"{message[:34]}" pages', len(posted) == 1))

    for message in ('Replication failover completed normally.',
                    'A nonfatal condition was observed and cleared.',
                    'A non-fatal condition was observed and cleared.',
                    'The load completed error-free.'):
        posted, _ = run_dms_event(message, SERVERLESS_ARN)
        checks.append((f'"{message[:34]}" is not critical', posted == []))
    return checks


def lifecycle_checks():
    """The resolve semantics: the red root is written once and never rewritten."""
    checks = []
    firing = {"message_ts": "111.222", "started_at": "2026-08-01T12:00:00.000+0000",
              "status": "ALARM"}

    updates, posts, states, reactions, claims, finals = deliver_cloudwatch("OK", firing)
    card = str(posts[0]['payload']) if posts else ''
    checks.extend([
        ("resolution posts one card", len(posts) == 1),
        ("resolution card is the full green card",
         posts and posts[0]['payload'].get('attachments', [{}])[0].get('color')
         == sns_to_slack.COLOR_RESOLVED
         and all(x in card for x in ('✅ RESOLVED', 'OUTCOME', 'DURATION', '8m'))),
        ("resolution threads under the root", posts and posts[0]['thread_ts'] == "111.222"),
        ("resolution broadcasts to the channel", posts and posts[0]['broadcast'] is True),
        # The audit that motivated this: 10 firing records overwritten in one week.
        ("resolution never edits the red root", updates == []),
        ("resolution reacts on the root", reactions == [("111.222", "white_check_mark")]),
        # Finalizing is now a conditional write of its own, not a blind PutItem, so it
        # has to carry the token proving we still own the claim we took.
        ("resolution finalises with the claim token it was given",
         finals == [(finals[0][0], 1754049600, finals[0][2])] if finals else False),
        ("resolution writes no unconditional state", states == []),
        ("resolution claims before it posts", len(claims) == 1),
    ])

    # reactions:write can be revoked, the root can be too old, Slack can 5xx. None of
    # that may cost us the resolution card, so the failure is swallowed at the call site
    # rather than allowed out of _deliver_cloudwatch_bot.
    try:
        updates, posts, states, reactions, claims, finals = deliver_cloudwatch(
            "OK", firing, reaction_error="missing_scope")
        escaped = False
    except Exception:  # noqa: BLE001 - the point of the check is that this cannot happen
        updates, posts, states, reactions, finals = [], [], [], [], []
        escaped = True
    checks.extend([
        ("reaction failure does not escape the handler", not escaped),
        ("reaction failure still posts the resolution card", len(posts) == 1),
        ("reaction failure does not edit the root", updates == []),
        # If this raised, the row would stay RESOLVING and the retry would re-post.
        ("reaction failure still finalises the resolution", len(finals) == 1),
    ])

    # The claim losing is what a duplicate or already-resolved OK looks like from here:
    # DynamoDB refused the ALARM -> RESOLVING transition. Nothing may be sent after that.
    updates, posts, states, reactions, claims, finals = deliver_cloudwatch(
        "OK", dict(firing, status="RESOLVED"), claim=False)
    checks.extend([
        ("a lost claim posts no card", posts == []),
        ("a lost claim adds no reaction", reactions == []),
        ("a lost claim edits nothing", updates == []),
        # Previously this rewrote the tombstone on every duplicate, pushing its TTL out
        # each time. Now it writes nothing at all.
        ("a lost claim writes no state", states == [] and finals == []),
        ("a lost claim still attempted the claim", len(claims) == 1),
    ])

    # Repeat ALARM keeps editing: it is duplicate SNS delivery of the same firing event,
    # and the edit is what stops the same incident appearing twice. It never goes green.
    updates, posts, states, reactions, claims, finals = deliver_cloudwatch("ALARM", firing)
    checks.extend([
        ("repeat ALARM edits the root", len(updates) == 1 and updates[0][0] == "111.222"),
        ("repeat ALARM posts nothing new", posts == []),
        ("repeat ALARM adds no reaction", reactions == []),
        ("repeat ALARM stays ALARM", bool(states) and states[0][3] == "ALARM"),
        ("repeat ALARM never claims a resolution", claims == []),
    ])

    updates, posts, states, reactions, claims, finals = deliver_cloudwatch("ALARM", None)
    checks.extend([
        ("first ALARM posts a root, unthreaded",
         len(posts) == 1 and posts[0]['thread_ts'] is None
         and posts[0]['broadcast'] is False),
        ("first ALARM edits nothing", updates == []),
    ])
    return checks


def ordering_checks():
    """Out-of-order delivery must not rewrite history with an older truth.

    SNS is at-least-once and unordered. The sequence that motivated this:

        ALARM1 -> OK1 -> ALARM2 -> (delayed) OK1

    the delayed OK1 used to thread a green card under ALARM2, react to it, and mark it
    RESOLVED - closing a live incident. The real OK2 was then suppressed because the row
    already said RESOLVED, so the incident that mattered never got a recovery at all.
    """
    checks = []
    # StateChangeTime of the row's last applied event, and one an hour older.
    newer = "2026-08-01T13:00:00.000+0000"
    older = "2026-08-01T12:00:00.000+0000"
    newer_epoch = sns_to_slack._event_epoch({"StateChangeTime": newer})

    open_incident = {"message_ts": "999.888", "started_at": newer,
                     "status": "ALARM", "last_event_at": newer_epoch}

    updates, posts, states, reactions, claims, finals = deliver_cloudwatch(
        "OK", open_incident, changed_at=older)
    checks.extend([
        ("a stale OK posts nothing", posts == []),
        ("a stale OK does not react on the live root", reactions == []),
        ("a stale OK does not close the live incident", states == []),
        # Dropped before the claim, so it costs no conditional write either.
        ("a stale OK never reaches the claim", claims == []),
    ])

    updates, posts, states, reactions, claims, finals = deliver_cloudwatch(
        "ALARM", open_incident, changed_at=older)
    checks.extend([
        ("a stale ALARM posts nothing", posts == []),
        ("a stale ALARM edits nothing", updates == []),
        ("a stale ALARM writes no state", states == []),
    ])

    # Same timestamp is a duplicate, not a stale event, and the two paths handle it
    # differently: ALARM edits the root, OK is gated by the claim. Neither is dropped
    # here, or a redelivery of the only OK we get would be lost.
    updates, posts, states, reactions, claims, finals = deliver_cloudwatch(
        "OK", open_incident, changed_at=newer)
    checks.append(("an equal-timestamp OK still reaches the claim", len(claims) == 1))

    # A row written before LastEventAt existed, or an event with no parseable
    # StateChangeTime, cannot be ordered. Both must fall through to posting rather than
    # being suppressed: never classify on missing data.
    legacy = {"message_ts": "111.222", "started_at": older, "status": "ALARM"}
    updates, posts, states, reactions, claims, finals = deliver_cloudwatch(
        "OK", legacy, changed_at=older)
    checks.append(("a row with no recorded event time still resolves", len(posts) == 1))

    updates, posts, states, reactions, claims, finals = deliver_cloudwatch(
        "OK", open_incident, changed_at="not a timestamp")
    checks.extend([
        ("an unparseable event time still resolves", len(posts) == 1),
        ("an unparseable event time claims with None",
         claims == [(claims[0][0], None)] if claims else False),
    ])

    checks.extend([
        ("_event_epoch parses CloudWatch's format", newer_epoch is not None),
        ("_event_epoch orders two timestamps",
         sns_to_slack._event_epoch({"StateChangeTime": older}) < newer_epoch),
        ("_event_epoch returns None on a missing field",
         sns_to_slack._event_epoch({}) is None),
        ("_event_epoch returns None on garbage",
         sns_to_slack._event_epoch({"StateChangeTime": "yesterday"}) is None),
        # fromisoformat happily parses a value with no offset, and .timestamp() would
        # then read it as process-local time - a confidently wrong ordering key built
        # from bad data. CloudWatch always sends an offset, so this belongs in None.
        ("_event_epoch rejects a timezone-naive timestamp",
         sns_to_slack._event_epoch({"StateChangeTime": "2026-08-01T12:00:00"}) is None),
        # A strptime format string with %f rejects this, which would have opted every
        # whole-second transition out of ordering without anything failing.
        ("_event_epoch parses a whole-second timestamp",
         sns_to_slack._event_epoch({"StateChangeTime": "2026-08-01T12:00:00+0000"})
         is not None),
        ("_event_epoch parses a Z suffix",
         sns_to_slack._event_epoch({"StateChangeTime": "2026-08-01T12:00:00.000Z"})
         is not None),
        ("whole-second and fractional forms of one instant agree",
         sns_to_slack._event_epoch({"StateChangeTime": "2026-08-01T12:00:00+0000"})
         == sns_to_slack._event_epoch(
             {"StateChangeTime": "2026-08-01T12:00:00.000+0000"})),
        ("_is_stale is False without a prior row",
         sns_to_slack._is_stale(newer_epoch, None) is False),
        ("_is_stale is False when the row has no recorded time",
         sns_to_slack._is_stale(newer_epoch, {"status": "ALARM"}) is False),
        ("_is_stale is False for an equal timestamp",
         sns_to_slack._is_stale(newer_epoch, open_incident) is False),
        ("_is_stale is True for an older event",
         sns_to_slack._is_stale(newer_epoch - 1, open_incident) is True),
    ])
    return checks


def claim_checks():
    """The conditional write _claim_resolution actually sends.

    DynamoDB evaluates the condition, not this suite, so what is pinned here is the
    request: the states it will accept, the lease comparison that lets a dead claim be
    taken over, and the event-time guard. A fake client stands in for the service and
    reports whether the condition would have been checked at all.
    """
    checks = []
    calls = []

    class _FakeDDB:
        exceptions = _FakeExceptions()

        def __init__(self, fail=False):
            self.fail = fail

        def update_item(self, **kwargs):
            calls.append(kwargs)
            if self.fail:
                raise _ConditionalCheckFailedException("condition not met")

    with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
         mock.patch.object(sns_to_slack.boto3, 'client', return_value=_FakeDDB()):
        won = sns_to_slack._claim_resolution('alarm-key', 1754049600.0)

    sent = calls[0] if calls else {}
    condition = sent.get('ConditionExpression', '')
    values = sent.get('ExpressionAttributeValues', {})
    checks.extend([
        ("a satisfied condition returns a claim token", bool(won) and won is not True),
        ("the claim writes RESOLVING",
         ':resolving' in values and values[':resolving'] == {'S': 'RESOLVING'}),
        ("the claim accepts an open ALARM", '#s = :alarm' in condition),
        ("the claim accepts a RESOLVING whose lease expired",
         '#s = :resolving AND ClaimedAt < :lease' in condition),
        ("the lease cutoff is CLAIM_LEASE_SECONDS old",
         int(values[':now']['N']) - int(values[':lease']['N'])
         == sns_to_slack.CLAIM_LEASE_SECONDS),
        ("the claim refuses an older event", 'LastEventAt <= :evt' in condition),
        ("the claim tolerates a row that predates LastEventAt",
         'attribute_not_exists(LastEventAt)' in condition),
        ("the claim records the event time", values.get(':evt') is not None),
    ])

    # A rejected condition has three possible causes and only two of them mean "post
    # nothing". Which one applies is read off the row, so each is driven separately.
    def claim_against(row):
        calls.clear()
        with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
             mock.patch.object(sns_to_slack.boto3, 'client',
                               return_value=_FakeDDB(fail=True)), \
             mock.patch.object(sns_to_slack, '_get_alarm_state', return_value=row):
            return sns_to_slack._claim_resolution('alarm-key', 1754049600.0)

    resolved_row = {'message_ts': '1.2', 'started_at': 'x', 'status': 'RESOLVED',
                    'last_event_at': 1754049600.0}
    still_firing = dict(resolved_row, status='ALARM')
    checks.extend([
        ("an already-RESOLVED row loses the claim without raising",
         claim_against(resolved_row) is None),
        ("a stale event against a live ALARM row loses the claim without raising",
         claim_against(still_firing) is None),
        ("a vanished row loses the claim without raising",
         claim_against(None) is None),
    ])

    # THE one that matters. An unexpired RESOLVING claim means another execution owns
    # this resolution and may have died mid-post. Returning False here would end the
    # invocation successfully, which drops the event from Lambda's async retry queue -
    # and Lambda's first retry lands at about the same minute as the lease, so the very
    # retry meant to take the claim over is the one that would be discarded. The card
    # would be lost for good. It must raise so the retry gets to re-read.
    held = dict(resolved_row, status='RESOLVING')
    raised = False
    try:
        claim_against(held)
    except RuntimeError:
        raised = True
    checks.append(("an unexpired RESOLVING claim raises so Lambda retries", raised))

    # No event time: there is nothing to order by, so the guard must come off entirely
    # rather than compare against a missing value and reject every claim.
    calls.clear()
    with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
         mock.patch.object(sns_to_slack.boto3, 'client', return_value=_FakeDDB()):
        sns_to_slack._claim_resolution('alarm-key', None)
    unordered = calls[0]['ConditionExpression'] if calls else ''
    checks.extend([
        ("an unorderable event drops the time guard", 'LastEventAt' not in unordered),
        ("an unorderable event still checks the status", '#s = :alarm' in unordered),
    ])

    # The webhook path has no table. Claiming must be a no-op rather than an error, and
    # must report False so nothing downstream believes it owns a resolution.
    calls.clear()
    with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', ''):
        checks.append(("no table configured means no claim and no call",
                       sns_to_slack._claim_resolution('k', 1.0) is None and calls == []))

    # A real service error is not a lost claim and must not be swallowed into one.
    calls.clear()

    class _BrokenDDB:
        exceptions = _FakeExceptions()

        def update_item(self, **kwargs):
            raise RuntimeError("ProvisionedThroughputExceededException")

    raised = False
    try:
        with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
             mock.patch.object(sns_to_slack.boto3, 'client', return_value=_BrokenDDB()):
            sns_to_slack._claim_resolution('k', 1.0)
    except RuntimeError:
        raised = True
    checks.append(("a non-condition failure propagates", raised))
    return checks


def finalize_checks():
    """Finalizing a resolution must not clobber an incident that superseded it.

    The flap this exists for: OK1 claims ALARM1 and starts posting; ALARM2 arrives, sees
    a RESOLVING row, posts its own red root and writes ALARM; OK1 then finishes. An
    unconditional write there would stamp RESOLVED over the live ALARM2, and ALARM2's
    real recovery would later find RESOLVED and post nothing - the original bug, moved
    one step later.
    """
    checks = []
    calls = []

    class _FakeDDB:
        exceptions = _FakeExceptions()

        def __init__(self, fail=False):
            self.fail = fail

        def update_item(self, **kwargs):
            calls.append(kwargs)
            if self.fail:
                raise _ConditionalCheckFailedException("superseded")

    with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
         mock.patch.object(sns_to_slack.boto3, 'client', return_value=_FakeDDB()):
        ok = sns_to_slack._finalize_resolution('alarm-key', 1754049600, 1754049601.0)

    sent = calls[0] if calls else {}
    condition = sent.get('ConditionExpression', '')
    values = sent.get('ExpressionAttributeValues', {})
    update = sent.get('UpdateExpression', '')
    checks.extend([
        ("finalising an owned claim succeeds", ok is True),
        ("finalising requires the row still be RESOLVING", '#s = :resolving' in condition),
        ("finalising requires the same claim token", 'ClaimedAt = :claim' in condition),
        ("the token is the one the claim returned",
         values.get(':claim') == {'N': '1754049600'}),
        ("finalising writes RESOLVED", ':resolved' in values and '#s = :resolved' in update),
        ("finalising sets the tombstone TTL", 'ExpiresAt = :expires' in update),
        ("the TTL is STATE_TTL_SECONDS out",
         int(values[':expires']['N']) - int(time.time())
         > sns_to_slack.STATE_TTL_SECONDS - 60),
        ("finalising advances the watermark", 'LastEventAt = :evt' in update),
    ])

    # Superseded: the card is already in the channel, so this is neither an error nor
    # retryable. Raising would only re-post it; writing anyway would eat the incident.
    calls.clear()
    raised = False
    try:
        with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
             mock.patch.object(sns_to_slack.boto3, 'client',
                               return_value=_FakeDDB(fail=True)):
            superseded = sns_to_slack._finalize_resolution('alarm-key', 1754049600, 1.0)
    except Exception:  # noqa: BLE001 - the point is that this cannot happen
        raised = True
        superseded = None
    checks.extend([
        ("a superseded finalise reports False", superseded is False),
        ("a superseded finalise does not raise", not raised),
    ])

    # An unorderable event must not advance or erase the watermark.
    calls.clear()
    with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
         mock.patch.object(sns_to_slack.boto3, 'client', return_value=_FakeDDB()):
        sns_to_slack._finalize_resolution('alarm-key', 1754049600, None)
    checks.append(("an unorderable finalise leaves the watermark alone",
                   'LastEventAt' not in calls[0]['UpdateExpression'] if calls else False))
    return checks


def watermark_checks():
    """A malformed event must never delete a good ordering watermark.

    _put_alarm_state replaces the whole item, so writing a row for an event that could
    not be ordered would drop LastEventAt entirely - and one bad timestamp would then
    disable ordering for every event after it.
    """
    written = []

    class _FakeDDB:
        def put_item(self, **kwargs):
            written.append(kwargs['Item'])

    prior = {'message_ts': '1.2', 'started_at': 'x', 'status': 'ALARM',
             'last_event_at': 1754049600.0}
    with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
         mock.patch.object(sns_to_slack.boto3, 'client', return_value=_FakeDDB()):
        sns_to_slack._put_alarm_state('k', '1.2', 'x', 'ALARM', None, prior)
        sns_to_slack._put_alarm_state('k', '1.2', 'x', 'ALARM', 1754049999.0, prior)
        sns_to_slack._put_alarm_state('k', '1.2', 'x', 'ALARM', None, None)

    carried, advanced, fresh = written
    return [
        ("an unorderable event keeps the previous watermark",
         carried.get('LastEventAt') == {'N': '1754049600.0'}),
        ("an orderable event advances the watermark",
         advanced.get('LastEventAt') == {'N': '1754049999.0'}),
        ("a first row with no orderable event writes no watermark",
         'LastEventAt' not in fresh),
    ]


def state_ttl_checks():
    """Only the tombstone carries a TTL.

    An ALARM row is the live correlation to the red root, not a tombstone. Expiring it
    strands the incident: the eventual recovery finds no prior and posts an unthreaded
    green root with DURATION: Unknown - the same record loss this change exists to stop,
    on a slower clock. These drive the real _put_alarm_state and read the item it builds.
    """
    written = []

    class _FakeDDB:
        def put_item(self, **kwargs):
            written.append(kwargs['Item'])

    checks = []
    with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'state-table'), \
         mock.patch.object(sns_to_slack.boto3, 'client', return_value=_FakeDDB()):
        sns_to_slack._put_alarm_state('k', '111.222', '2026-08-01T12:00:00+00:00', 'ALARM')
        sns_to_slack._put_alarm_state('k', '111.222', '2026-08-01T12:00:00+00:00', 'RESOLVED')

    alarm_item, resolved_item = written
    checks.extend([
        ("an ALARM row carries no TTL", 'ExpiresAt' not in alarm_item),
        ("an ALARM row still records its root ts", alarm_item['MessageTs']['S'] == '111.222'),
        ("a RESOLVED row carries a TTL", 'ExpiresAt' in resolved_item),
        ("the RESOLVED TTL is in the future",
         int(resolved_item['ExpiresAt']['N']) > int(time.time())),
    ])
    return checks


def renderer_checks():
    checks = []

    active, _ = sns_to_slack.cloudwatch_card(
        alarm_fixture(), "acme-prod", "us-east-1", "111", "prod")
    active_text = str(active)
    checks.extend([
        ("critical rail", active["attachments"][0]["color"] == sns_to_slack.COLOR_CRITICAL),
        ("critical header", "🔴 CRITICAL · CloudWatch · acme-prod" in active_text),
        ("critical UX sections", all(x in active_text for x in
                                     ("IMPACT", "SERVICE", "RESOURCE", "EVIDENCE"))),
        ("critical real actions", "Open CloudWatch" in active_text and "Runbook" in active_text),
        ("critical pages channel", "<!channel>" in active_text),
    ])

    resolved, _ = sns_to_slack.cloudwatch_card(
        alarm_fixture("OK"), "acme-prod", "us-east-1", "111", "prod",
        started_at="2026-08-01T12:00:00.000+0000")
    resolved_text = str(resolved)
    checks.extend([
        ("resolved rail", resolved["attachments"][0]["color"] == sns_to_slack.COLOR_RESOLVED),
        ("resolved header", "✅ RESOLVED · CloudWatch · acme-prod" in resolved_text),
        ("resolved outcome", all(x in resolved_text for x in ("OUTCOME", "DURATION", "8m"))),
        ("resolved does not page", "<!channel>" not in resolved_text),
    ])

    dms_event = {"detail-type": "DMS Replication State Change",
                 "detail": {"detailMessage": "DMS replication has failed."}}
    dms = sns_to_slack.dms_card(
        dms_event, "acme-prod", "us-east-1", "111", "prod", "bi-replication",
        True, "https://console.aws.amazon.com/dms")
    dms_text = str(dms)
    checks.extend([
        ("DMS unified header", "🔴 CRITICAL · DMS · acme-prod" in dms_text),
        ("DMS unified actions", "Open DMS" in dms_text),
    ])

    fallback = sns_to_slack.unknown_card(
        {"schema": "secret raw payload"}, "acme-prod", "us-east-1")
    checks.append(("unknown payload stays out of Slack", "secret raw payload" not in str(fallback)))

    return checks


def main():
    failures = []
    for message, expected, why in CASES:
        got = classify(message, resource_arn=TASK_ARN)
        if got != expected:
            failures.append((message, expected, got, f"provisioned: {why}"))

    for message, expected, why in SERVERLESS_CASES:
        got = classify(message, resource_arn=SERVERLESS_ARN)
        if got != expected:
            failures.append((message, expected, got, f"serverless: {why}"))

    checks = (renderer_checks() + slack_body_checks() + dms_routing_checks()
              + lifecycle_checks() + state_ttl_checks() + ordering_checks()
              + claim_checks() + finalize_checks() + watermark_checks())
    for name, passed in checks:
        if not passed:
            failures.append((name, "PASS", "FAIL", "renderer/routing/lifecycle contract"))

    for message, expected, got, why in failures:
        print(f"FAIL  expected {expected}, got {got}\n      {message[:100]}\n      ({why})")

    total = len(CASES) + len(SERVERLESS_CASES) + len(checks)
    print(f"\n{total - len(failures)}/{total} passed")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
