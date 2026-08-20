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
from types import SimpleNamespace
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


def alarm_fixture(state="ALARM", alarm_name="acme-prod-api-5xx"):
    return {
        "AlarmName": alarm_name,
        "AlarmDescription": "Checkout API error rate is elevated. https://runbooks.example/api-5xx",
        "AlarmArn": f"arn:aws:cloudwatch:us-east-1:111:alarm:{alarm_name}",
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
                       changed_at=None, already_posted=False,
                       state_table='state-table'):
    """Run _deliver_cloudwatch_bot with every Slack and DynamoDB call captured.

    claim stands in for the conditional write's verdict: True means this delivery won
    the ALARM -> RESOLVING transition, False means DynamoDB rejected it because another
    delivery already owns the resolve or the event is older than the row. The condition
    itself is DynamoDB's to evaluate - what these checks pin is that we ask before
    posting and honour the answer. _claim_resolution's request shape is asserted
    separately in claim_checks().
    """
    updates, posts, states, reactions, claims, finals = [], [], [], [], [], []
    root_claims, root_finals, dedupes = [], [], []

    def _post(token, payload, thread_ts=None, reply_broadcast=False, transition=None):
        posts.append({'payload': payload, 'thread_ts': thread_ts,
                      'broadcast': reply_broadcast, 'transition': transition})
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

    def _claim_root(alarm_key, event_at):
        root_claims.append((alarm_key, event_at))
        return 1754049600 if claim else None

    def _finalize_root(alarm_key, claim_token, message_ts, started_at, event_at):
        root_finals.append((alarm_key, claim_token, message_ts))
        return True

    def _posted(token, marker, thread_ts=None, since=None):
        dedupes.append((marker, thread_ts))
        return '888.999' if already_posted else None

    fixture = alarm_fixture(state)
    if changed_at is not None:
        fixture['StateChangeTime'] = changed_at

    with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', state_table), \
         mock.patch.object(sns_to_slack, '_get_alarm_state', return_value=prior), \
         mock.patch.object(sns_to_slack, '_update_bot',
                           side_effect=lambda token, ts, payload: updates.append((ts, payload))), \
         mock.patch.object(sns_to_slack, '_post_bot', side_effect=_post), \
         mock.patch.object(sns_to_slack, '_add_reaction', side_effect=_react), \
         mock.patch.object(sns_to_slack, '_claim_resolution', side_effect=_claim), \
         mock.patch.object(sns_to_slack, '_finalize_resolution', side_effect=_finalize), \
         mock.patch.object(sns_to_slack, '_claim_alarm_root', side_effect=_claim_root), \
         mock.patch.object(sns_to_slack, '_finalize_alarm_root', side_effect=_finalize_root), \
         mock.patch.object(sns_to_slack, '_already_posted', side_effect=_posted), \
         mock.patch.object(sns_to_slack, '_touch_alarm_watermark',
                           side_effect=lambda *args: states.append(args) or True):
        sns_to_slack._deliver_cloudwatch_bot(
            'xoxb', fixture, 'acme-prod', 'us-east-1', '111', 'prod')
    return SimpleNamespace(
        updates=updates, posts=posts, states=states, reactions=reactions,
        claims=claims, finals=finals, root_claims=root_claims,
        root_finals=root_finals, dedupes=dedupes)


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

    r = deliver_cloudwatch("OK", firing)
    card = str(r.posts[0]['payload']) if r.posts else ''
    checks.extend([
        ("resolution posts one card", len(r.posts) == 1),
        ("resolution card is the full green card",
         r.posts and r.posts[0]['payload'].get('attachments', [{}])[0].get('color')
         == sns_to_slack.COLOR_RESOLVED
         and all(x in card for x in ('✅ RESOLVED', 'OUTCOME', 'DURATION', '8m'))),
        ("resolution threads under the root", r.posts and r.posts[0]['thread_ts'] == "111.222"),
        ("resolution broadcasts to the channel", r.posts and r.posts[0]['broadcast'] is True),
        # The audit that motivated this: 10 firing records overwritten in one week.
        ("resolution never edits the red root", r.updates == []),
        ("resolution reacts on the root", r.reactions == [("111.222", "white_check_mark")]),
        # Finalizing is now a conditional write of its own, not a blind PutItem, so it
        # has to carry the token proving we still own the claim we took.
        ("resolution finalises with the claim token it was given",
         r.finals == [(r.finals[0][0], 1754049600, r.finals[0][2])] if r.finals else False),
        ("resolution writes no unconditional state", r.states == []),
        ("resolution claims before it posts", len(r.claims) == 1),
    ])

    # reactions:write can be revoked, the root can be too old, Slack can 5xx. None of
    # that may cost us the resolution card, so the failure is swallowed at the call site
    # rather than allowed out of _deliver_cloudwatch_bot.
    try:
        r = deliver_cloudwatch(
            "OK", firing, reaction_error="missing_scope")
        escaped = False
    except Exception:  # noqa: BLE001 - the point of the check is that this cannot happen
        r = SimpleNamespace(updates=[], posts=[], states=[], reactions=[],
                            claims=[], finals=[], root_claims=[],
                            root_finals=[], dedupes=[])
        escaped = True
    checks.extend([
        ("reaction failure does not escape the handler", not escaped),
        ("reaction failure still posts the resolution card", len(r.posts) == 1),
        ("reaction failure does not edit the root", r.updates == []),
        # If this raised, the row would stay RESOLVING and the retry would re-post.
        ("reaction failure still finalises the resolution", len(r.finals) == 1),
    ])

    # The claim losing is what a duplicate or already-resolved OK looks like from here:
    # DynamoDB refused the ALARM -> RESOLVING transition. Nothing may be sent after that.
    r = deliver_cloudwatch(
        "OK", dict(firing, status="RESOLVED"), claim=False)
    checks.extend([
        ("a lost claim posts no card", r.posts == []),
        ("a lost claim adds no reaction", r.reactions == []),
        ("a lost claim edits nothing", r.updates == []),
        # Previously this rewrote the tombstone on every duplicate, pushing its TTL out
        # each time. Now it writes nothing at all.
        ("a lost claim writes no state", r.states == [] and r.finals == []),
        ("a lost claim still attempted the claim", len(r.claims) == 1),
    ])

    # Repeat ALARM keeps editing: it is duplicate SNS delivery of the same firing event,
    # and the edit is what stops the same incident appearing twice. It never goes green.
    r = deliver_cloudwatch("ALARM", firing)
    checks.extend([
        ("repeat ALARM edits the root", len(r.updates) == 1 and r.updates[0][0] == "111.222"),
        ("repeat ALARM posts nothing new", r.posts == []),
        ("repeat ALARM adds no reaction", r.reactions == []),
        # The blind whole-item write is gone: a duplicate now touches ONLY the
        # watermark, conditional on the row still holding this same root.
        ("repeat ALARM touches only the watermark, for its own root",
         r.states == [(r.states[0][0], "111.222", r.states[0][2])]
         if r.states else False),
        ("repeat ALARM never claims a resolution", r.claims == []),
    ])

    r = deliver_cloudwatch("ALARM", None)
    checks.extend([
        ("first ALARM posts a root, unthreaded",
         len(r.posts) == 1 and r.posts[0]['thread_ts'] is None
         and r.posts[0]['broadcast'] is False),
        ("first ALARM edits nothing", r.updates == []),
        # Same reasoning as the resolve: two deliveries that both see no row would
        # otherwise both post a root, and only one timestamp would survive the write.
        ("first ALARM claims before it posts", len(r.root_claims) == 1),
        ("first ALARM finalises with its claim token",
         r.root_finals == [(r.root_finals[0][0], 1754049600, '333.444')]
         if r.root_finals else False),
        ("first ALARM writes no unconditional state", r.states == []),
        # A fresh claim cannot have a root already in the channel, so no read is spent.
        ("first ALARM does not read Slack history", r.dedupes == []),
    ])

    r = deliver_cloudwatch("ALARM", None, claim=False)
    checks.extend([
        ("a lost root claim posts nothing", r.posts == []),
        ("a lost root claim finalises nothing", r.root_finals == []),
    ])

    # Taking over an ALARM_POSTING claim means a previous attempt died. It may have got
    # its root into the channel first, and only Slack can say.
    posting = {"message_ts": "", "started_at": "2026-08-01T12:00:00.000+0000",
               "status": "ALARM_POSTING"}
    r = deliver_cloudwatch("ALARM", posting, already_posted=True)
    checks.extend([
        ("a takeover checks Slack before reposting the root", len(r.dedupes) == 1),
        ("a root already in the channel is not posted twice", r.posts == []),
    ])

    r = deliver_cloudwatch("ALARM", posting, already_posted=False)
    checks.append(("a takeover with no root in the channel posts one", len(r.posts) == 1))

    # The resolve half of the same problem: the lease turns every crash into a second
    # green card unless the thread is checked first.
    resolving = {"message_ts": "111.222", "started_at": "2026-08-01T12:00:00.000+0000",
                 "status": "RESOLVING"}
    r = deliver_cloudwatch("OK", resolving, already_posted=True)
    checks.extend([
        # Matched on the transition id in message metadata, not on "RESOLVED" text:
        # a substring cannot tell this incident's card from the previous one's.
        ("a resolve takeover checks the thread for THIS transition",
         len(r.dedupes) == 1 and r.dedupes[0][1] == "111.222"
         and r.dedupes[0][0].startswith("arn:aws:cloudwatch")
         and r.dedupes[0][0].endswith("|OK|2026-08-01T12:08:00.000+0000")),
        ("a recovery already in the thread is not posted twice", r.posts == []),
        ("a recovery already in the thread still finalises", len(r.finals) == 1),
        # The attempt that died may have posted and gone before reacting, leaving the
        # root red forever in scrollback. reactions.add tolerates already_reacted.
        ("a recovery already in the thread is still reacted to",
         r.reactions == [("111.222", "white_check_mark")]),
    ])

    r = deliver_cloudwatch("OK", resolving, already_posted=False)
    checks.append(("a resolve takeover with an empty thread posts the card",
                   len(r.posts) == 1))

    # An abandoned root can recover as an unthreaded green card. If that invocation
    # then dies before finalising, its retry finds the green card in channel history but
    # still has no red root timestamp to react to.
    orphaned_resolving = {
        "message_ts": None,
        "started_at": "2026-08-01T12:00:00.000+0000",
        "status": "RESOLVING",
    }
    r = deliver_cloudwatch("OK", orphaned_resolving, already_posted=True)
    checks.extend([
        ("an orphaned recovery retry does not react without a root ts",
         r.reactions == []),
        ("an orphaned recovery retry still finalises", len(r.finals) == 1),
    ])

    # The common path must not spend a Slack read: a claim taken from a live ALARM row
    # is a first attempt, and nothing can already be in the thread.
    r = deliver_cloudwatch("OK", firing)
    checks.append(("a first-attempt resolve does not read the thread", r.dedupes == []))

    # Adopting a root someone else posted is only half the job. Without the finalise the
    # row sits in ALARM_POSTING with no MessageTs forever, and every later OK fails to
    # claim it - a dedupe that quietly bricks the incident.
    r = deliver_cloudwatch("ALARM", posting, already_posted=True)
    checks.extend([
        ("an adopted root is still finalised", len(r.root_finals) == 1),
        ("the adopted root's ts is what gets recorded",
         r.root_finals[0][2] == '888.999' if r.root_finals else False),
    ])

    # A bot token with no state table is a valid configuration: no correlation, no
    # claims, but the cards still have to go out. Guarding on the claim alone turned
    # this into a total blackout, which is far worse than the unthreaded posting it
    # replaced.
    r = deliver_cloudwatch("ALARM", None, claim=False, state_table='')
    checks.extend([
        ("no state table still posts the ALARM root", len(r.posts) == 1),
        # This previously read `r.root_finals == [] or True`, which can never fail. The
        # real no-op with no table is asserted against DynamoDB in dynamo_checks; what
        # matters HERE is only that the card still goes out.
        ("no state table still posts unthreaded",
         r.posts and r.posts[0]['thread_ts'] is None),
    ])

    r = deliver_cloudwatch("OK", firing, claim=False, state_table='')
    checks.append(("no state table still posts the recovery", len(r.posts) == 1))
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

    r = deliver_cloudwatch(
        "OK", open_incident, changed_at=older)
    checks.extend([
        ("a stale OK posts nothing", r.posts == []),
        ("a stale OK does not react on the live root", r.reactions == []),
        ("a stale OK does not close the live incident", r.states == []),
        # Dropped before the claim, so it costs no conditional write either.
        ("a stale OK never reaches the claim", r.claims == []),
    ])

    r = deliver_cloudwatch(
        "ALARM", open_incident, changed_at=older)
    checks.extend([
        ("a stale ALARM posts nothing", r.posts == []),
        ("a stale ALARM edits nothing", r.updates == []),
        ("a stale ALARM writes no state", r.states == []),
    ])

    # Same timestamp is a duplicate, not a stale event, and the two paths handle it
    # differently: ALARM edits the root, OK is gated by the claim. Neither is dropped
    # here, or a redelivery of the only OK we get would be lost.
    r = deliver_cloudwatch(
        "OK", open_incident, changed_at=newer)
    checks.append(("an equal-timestamp OK still reaches the claim", len(r.claims) == 1))

    # A row written before LastEventAt existed, or an event with no parseable
    # StateChangeTime, cannot be ordered. Both must fall through to posting rather than
    # being suppressed: never classify on missing data.
    legacy = {"message_ts": "111.222", "started_at": older, "status": "ALARM"}
    r = deliver_cloudwatch(
        "OK", legacy, changed_at=older)
    checks.append(("a row with no recorded event time still resolves", len(r.posts) == 1))

    # An event with no usable timestamp cannot be placed against the row, so it fails
    # open for VISIBILITY only: post it, but do not claim, react to, or rewrite a live
    # incident. Letting it through to the claim is how a delayed unparseable OK used to
    # close an incident that was still burning.
    r = deliver_cloudwatch("OK", open_incident, changed_at="not a timestamp")
    checks.extend([
        ("an unparseable event time still posts something", len(r.posts) == 1),
        ("it posts uncorrelated, not threaded", r.posts[0]['thread_ts'] is None),
        ("it never claims the live incident", r.claims == []),
        ("it does not react on the live root", r.reactions == []),
        ("it writes no state", r.states == [] and r.finals == []),
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


# --- DynamoDB-backed checks -------------------------------------------------------
#
# Everything above this line runs on mocks, which is right for routing and rendering.
# The state machine is different: its correctness lives in ConditionExpressions that
# DynamoDB evaluates, and a mock that asserts "we sent the right request" cannot tell a
# correct condition from a nonsense one. State-transition defects are only visible when
# something actually executes the condition, so these checks run against moto.

try:
    import moto
    _MOTO = True
except ImportError:  # pragma: no cover - the CI job installs it
    _MOTO = False


# Hermetic by construction. The module under test builds its clients with a bare
# boto3.client('dynamodb'), which in the real Lambda picks the region up from the
# runtime - but on a machine with no AWS config that raises NoRegionError, so the suite
# would pass for whoever happened to have a region set and fail in CI. Pinning fake
# values here means these checks never touch a real account and never depend on one.
_FAKE_AWS_ENV = {
    'AWS_DEFAULT_REGION': 'us-east-1',
    'AWS_REGION': 'us-east-1',
    'AWS_ACCESS_KEY_ID': 'testing',
    'AWS_SECRET_ACCESS_KEY': 'testing',
    'AWS_SECURITY_TOKEN': 'testing',
    'AWS_SESSION_TOKEN': 'testing',
}


def _with_table(fn):
    """Run fn(client, table) against a fresh in-memory DynamoDB table."""
    import boto3
    with mock.patch.dict(os.environ, _FAKE_AWS_ENV), moto.mock_aws():
        client = boto3.client('dynamodb', region_name='us-east-1')
        client.create_table(
            TableName='slack-alarm-state',
            KeySchema=[{'AttributeName': 'AlarmKey', 'KeyType': 'HASH'}],
            AttributeDefinitions=[{'AttributeName': 'AlarmKey',
                                   'AttributeType': 'S'}],
            BillingMode='PAY_PER_REQUEST')
        with mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', 'slack-alarm-state'):
            return fn(client, 'slack-alarm-state')


def _row(client):
    got = client.get_item(TableName='slack-alarm-state',
                          Key={'AlarmKey': {'S': 'k'}}).get('Item') or {}
    return {k: list(v.values())[0] for k, v in got.items()}


def dedupe_checks():
    """What _already_posted will and will not treat as an existing card."""
    def read_returning(messages, marker='arn:k|ALARM|T1'):
        with mock.patch.object(sns_to_slack, 'SLACK_CHANNEL_ID', 'C1'), \
             mock.patch.object(sns_to_slack, '_slack_get',
                               return_value={'messages': messages}):
            return sns_to_slack._already_posted('xoxb', marker)

    def bot_card(transition, ts='5.6'):
        return {'bot_id': 'B1', 'ts': ts, 'text': 'CRITICAL acme-prod-api-5xx',
                'metadata': {'event_type': 'cloudwatch_alarm',
                             'event_payload': {'transition': transition}}}

    checks = [
        ("this transition's own card is found, and its ts returned",
         read_returning([bot_card('arn:k|ALARM|T1')]) == '5.6'),
        # The reason substring matching had to go: during a fast re-alarm the previous
        # incident's red root and green broadcast are both still in the window and both
        # still contain the alarm name. Adopting one of those as this incident's root
        # suppresses the card that actually needed sending.
        ("the PREVIOUS firing's card is not adopted",
         read_returning([bot_card('arn:k|ALARM|T0')]) is None),
        ("a recovery card is not adopted as a root",
         read_returning([bot_card('arn:k|OK|T1')]) is None),
        # A human typing the alarm name while triaging must not suppress the card they
        # are talking about - fail-closed hiding inside a fail-open design.
        ("a human message is never a match",
         read_returning([{'user': 'U1', 'ts': '9.9',
                          'text': 'is acme-prod-api-5xx still firing?',
                          'metadata': {'event_payload':
                                       {'transition': 'arn:k|ALARM|T1'}}}]) is None),
        # Matching must be equality on the metadata field, not "does this id appear
        # anywhere in the message". A card that merely QUOTES another transition's id -
        # or whose own id contains this one as a prefix - is not this transition's card.
        ("an id quoted in the text is not a match when the metadata differs",
         read_returning([{'bot_id': 'B1', 'ts': '7.7',
                          'text': 'retrying arn:k|ALARM|T1',
                          'metadata': {'event_payload':
                                       {'transition': 'arn:k|ALARM|T9'}}}]) is None),
        ("a longer id containing this one as a prefix is not a match",
         read_returning([bot_card('arn:k|ALARM|T10')], marker='arn:k|ALARM|T1') is None),
        ("a bot message with no metadata is not a match",
         read_returning([{'bot_id': 'B1', 'ts': '5.6', 'text': 'acme-prod-api-5xx'}])
         is None),
    ]

    # A read failure must fail OPEN: returning a ts would suppress a card never sent.
    with mock.patch.object(sns_to_slack, 'SLACK_CHANNEL_ID', 'C1'), \
         mock.patch.object(sns_to_slack, '_slack_get',
                           side_effect=RuntimeError('missing_scope')):
        checks.append(("a failed history read fails open",
                       sns_to_slack._already_posted('xoxb', 'x') is None))

    # The read has to ask for metadata or the match can never succeed.
    asked = {}
    with mock.patch.object(sns_to_slack, 'SLACK_CHANNEL_ID', 'C1'), \
         mock.patch.object(sns_to_slack, '_slack_get',
                           side_effect=lambda m, t, params: asked.update(
                               {m: params}) or {'messages': []}):
        sns_to_slack._already_posted('xoxb', 'x')
        sns_to_slack._already_posted('xoxb', 'x', thread_ts='1.1')
    checks.extend([
        ("the history read requests metadata",
         asked.get('conversations.history', {}).get('include_all_metadata') == 'true'),
        ("the thread read requests metadata",
         asked.get('conversations.replies', {}).get('include_all_metadata') == 'true'),
    ])
    return checks


def dynamo_checks():
    """The state machine, executed against a real DynamoDB implementation."""
    if not _MOTO:
        # Deliberately a FAILURE, not a skip. Without moto none of the conditional
        # writes are exercised at all, and a suite that silently drops its only real
        # coverage of them while still reporting green is how this got here.
        return [("moto is installed so the state machine can be tested for real", False)]
    checks = []

    # A claimed-but-unposted row has no MessageTs. Reading it used to raise KeyError,
    # which crashed every retry BEFORE it could take the claim over - so a died-mid-post
    # root was lost permanently and the lease never got to matter.
    def pending_read(client, table):
        token = sns_to_slack._claim_alarm_root('k', 100.0)
        return token, sns_to_slack._get_alarm_state('k'), _row(client)
    token, state, row = _with_table(pending_read)
    checks.extend([
        ("a fresh root claim succeeds", bool(token)),
        ("the pending row it writes is readable", state is not None),
        ("a pending row reports no message ts", state and state['message_ts'] is None),
        ("a pending row reports its status", state and state['status'] == 'ALARM_POSTING'),
        ("the claim token is a uuid, not a timestamp",
         len(token) == 32 and not token.isdigit()),
        ("the row carries that token", row.get('ClaimToken') == token),
    ])

    # Two claims inside one wall-clock second must not share an identity. ClaimedAt is
    # whole seconds, so using it as the token let a stale finalizer satisfy a condition
    # written for a claim it never held, and close someone else's incident.
    def two_claims(client, table):
        a = sns_to_slack._claim_alarm_root('k', 100.0)
        client.update_item(TableName=table, Key={'AlarmKey': {'S': 'k'}},
                           UpdateExpression='SET #s = :a',
                           ExpressionAttributeNames={'#s': 'Status'},
                           ExpressionAttributeValues={':a': {'S': 'RESOLVED'}})
        b = sns_to_slack._claim_alarm_root('k', 101.0)
        return a, b
    a, b = _with_table(two_claims)
    checks.append(("two claims in the same second get different tokens", a != b))

    # A finalizer holding a superseded token must not win.
    def stale_finalize(client, table):
        first = sns_to_slack._claim_alarm_root('k', 100.0)
        sns_to_slack._finalize_alarm_root('k', first, '1.1', 'start', 100.0)
        # A second incident takes the row over. It has to resolve first - a row still
        # in ALARM is the repeat-delivery path, not a new incident.
        ok = sns_to_slack._claim_resolution('k', 150.0)
        sns_to_slack._finalize_resolution('k', ok, 150.0)
        second = sns_to_slack._claim_alarm_root('k', 200.0)
        # The first claim's finaliser arrives late.
        late = sns_to_slack._finalize_alarm_root('k', first, '9.9', 'start', 100.0)
        return late, second, _row(client)
    late, second, row = _with_table(stale_finalize)
    checks.extend([
        ("a superseded finaliser is rejected", late is False),
        ("the newer claim still owns the row", row.get('ClaimToken') == second),
        ("the superseded finaliser did not write its message ts",
         row.get('MessageTs') != '9.9'),
    ])

    # A paused duplicate ALARM must not resurrect an incident that has since resolved
    # and been replaced.
    def duplicate_overwrite(client, table):
        first = sns_to_slack._claim_alarm_root('k', 100.0)
        sns_to_slack._finalize_alarm_root('k', first, 'root-1', 'start', 100.0)
        ok = sns_to_slack._claim_resolution('k', 200.0)
        sns_to_slack._finalize_resolution('k', ok, 200.0)
        second = sns_to_slack._claim_alarm_root('k', 300.0)
        sns_to_slack._finalize_alarm_root('k', second, 'root-2', 'start', 300.0)
        # The paused duplicate of incident ONE finally lands.
        touched = sns_to_slack._touch_alarm_watermark('k', 'root-1', 100.0)
        return touched, _row(client)
    touched, row = _with_table(duplicate_overwrite)
    checks.extend([
        ("a stale duplicate ALARM cannot touch the row", touched is False),
        ("the live incident keeps its root", row.get('MessageTs') == 'root-2'),
        ("the live incident keeps its watermark", float(row.get('LastEventAt')) == 300.0),
    ])

    # A duplicate of the CURRENT incident may advance the watermark.
    def live_duplicate(client, table):
        first = sns_to_slack._claim_alarm_root('k', 100.0)
        sns_to_slack._finalize_alarm_root('k', first, 'root-1', 'start', 100.0)
        return sns_to_slack._touch_alarm_watermark('k', 'root-1', 150.0), _row(client)
    touched, row = _with_table(live_duplicate)
    checks.extend([
        ("a duplicate of the live incident is applied", touched is True),
        ("it advances the watermark", float(row.get('LastEventAt')) == 150.0),
    ])

    # Ordering, evaluated by the service rather than asserted as a substring.
    def stale_resolve(client, table):
        first = sns_to_slack._claim_alarm_root('k', 300.0)
        sns_to_slack._finalize_alarm_root('k', first, 'root-1', 'start', 300.0)
        return sns_to_slack._claim_resolution('k', 200.0)
    checks.append(("an OK older than the watermark cannot claim",
                   _with_table(stale_resolve) is None))

    def lease(client, table):
        sns_to_slack._claim_alarm_root('k', 100.0)
        client.update_item(TableName=table, Key={'AlarmKey': {'S': 'k'}},
                           UpdateExpression='SET ClaimedAt = :old',
                           ExpressionAttributeValues={':old': {'N': str(
                               int(time.time()) - sns_to_slack.CLAIM_LEASE_SECONDS - 5)}})
        return sns_to_slack._claim_alarm_root('k', 101.0)
    checks.append(("an expired claim can be taken over", bool(_with_table(lease))))

    def unexpired(client, table):
        sns_to_slack._claim_alarm_root('k', 100.0)
        try:
            sns_to_slack._claim_alarm_root('k', 101.0)
            return 'no raise'
        except RuntimeError:
            return 'raised'
    checks.append(("an unexpired claim raises instead of dropping the event",
                   _with_table(unexpired) == 'raised'))

    # The handler checks the watermark before it tries to reclaim an expired lease. A
    # claim must therefore record the state that owns its new watermark. Otherwise a
    # retry of that same event sees an equal timestamp paired with the previous state,
    # classifies itself as a conflict, and never reaches the takeover condition.
    def crashed_root_retry(client, table):
        event = alarm_fixture('ALARM')
        event_at = sns_to_slack._event_epoch(event)
        alarm_key = event['AlarmArn']
        old_root = sns_to_slack._claim_alarm_root(alarm_key, 100.0)
        sns_to_slack._finalize_alarm_root(
            alarm_key, old_root, 'old-root', 'start', 100.0)
        old_ok = sns_to_slack._claim_resolution(alarm_key, 200.0)
        sns_to_slack._finalize_resolution(alarm_key, old_ok, 200.0)

        first = sns_to_slack._claim_alarm_root(event['AlarmArn'], event_at)
        claimed = client.get_item(
            TableName=table,
            Key={'AlarmKey': {'S': event['AlarmArn']}},
            ConsistentRead=True,
        )['Item']
        client.update_item(
            TableName=table,
            Key={'AlarmKey': {'S': event['AlarmArn']}},
            UpdateExpression='SET ClaimedAt = :old',
            ExpressionAttributeValues={':old': {'N': str(
                int(time.time()) - sns_to_slack.CLAIM_LEASE_SECONDS - 5)}},
        )
        posted = []
        with mock.patch.object(sns_to_slack, '_already_posted', return_value=None), \
             mock.patch.object(sns_to_slack, '_post_bot',
                               side_effect=lambda *args, **kwargs:
                               posted.append(kwargs) or 'new-root'):
            sns_to_slack._deliver_cloudwatch_bot(
                'xoxb', event, 'acme-prod', 'us-east-1', '111', 'prod')
        final = client.get_item(
            TableName=table,
            Key={'AlarmKey': {'S': event['AlarmArn']}},
            ConsistentRead=True,
        )['Item']
        return first, claimed, posted, final

    first, claimed, posted, final = _with_table(crashed_root_retry)
    checks.extend([
        ("a pending root claim records the ALARM state for its watermark",
         claimed.get('LastEventState') == {'S': 'ALARM'}),
        ("a pending root claim clears the resolved tombstone TTL",
         'ExpiresAt' not in claimed),
        ("the same ALARM retries after its expired claim",
         bool(first) and len(posted) == 1 and final.get('Status') == {'S': 'ALARM'}),
    ])

    def crashed_resolve_retry(client, table):
        event = alarm_fixture('OK')
        event_at = sns_to_slack._event_epoch(event)
        root = sns_to_slack._claim_alarm_root(event['AlarmArn'], event_at - 60)
        sns_to_slack._finalize_alarm_root(
            event['AlarmArn'], root, 'root', event['StateChangeTime'], event_at - 60)
        first = sns_to_slack._claim_resolution(event['AlarmArn'], event_at)
        claimed = client.get_item(
            TableName=table,
            Key={'AlarmKey': {'S': event['AlarmArn']}},
            ConsistentRead=True,
        )['Item']
        client.update_item(
            TableName=table,
            Key={'AlarmKey': {'S': event['AlarmArn']}},
            UpdateExpression='SET ClaimedAt = :old',
            ExpressionAttributeValues={':old': {'N': str(
                int(time.time()) - sns_to_slack.CLAIM_LEASE_SECONDS - 5)}},
        )
        posted = []
        with mock.patch.object(sns_to_slack, '_already_posted', return_value=None), \
             mock.patch.object(sns_to_slack, '_post_bot',
                               side_effect=lambda *args, **kwargs:
                               posted.append(kwargs) or 'recovery'), \
             mock.patch.object(sns_to_slack, '_add_reaction'):
            sns_to_slack._deliver_cloudwatch_bot(
                'xoxb', event, 'acme-prod', 'us-east-1', '111', 'prod')
        final = client.get_item(
            TableName=table,
            Key={'AlarmKey': {'S': event['AlarmArn']}},
            ConsistentRead=True,
        )['Item']
        return first, claimed, posted, final

    first, claimed, posted, final = _with_table(crashed_resolve_retry)
    checks.extend([
        ("a pending resolve claim records the OK state for its watermark",
         claimed.get('LastEventState') == {'S': 'OK'}),
        ("the same OK retries after its expired claim",
         bool(first) and len(posted) == 1 and final.get('Status') == {'S': 'RESOLVED'}),
    ])

    # An OK can arrive after the invocation that claimed the red root exhausted its own
    # retries. Once that root lease expires, the recovery must break the wedge. There is
    # no trustworthy root ts to thread under, so the safe fallback is one visible,
    # unthreaded recovery card with no reaction or broadcast-only Slack arguments.
    def ok_after_abandoned_root(client, table):
        alarm = alarm_fixture('ALARM')
        alarm_at = sns_to_slack._event_epoch(alarm)
        sns_to_slack._claim_alarm_root(alarm['AlarmArn'], alarm_at)
        client.update_item(
            TableName=table,
            Key={'AlarmKey': {'S': alarm['AlarmArn']}},
            UpdateExpression='SET ClaimedAt = :old',
            ExpressionAttributeValues={':old': {'N': str(
                int(time.time()) - sns_to_slack.CLAIM_LEASE_SECONDS - 5)}},
        )

        posted, reactions = [], []
        error = None

        def post(*args, **kwargs):
            posted.append(kwargs)
            return 'recovery-root'

        try:
            with mock.patch.object(sns_to_slack, '_post_bot', side_effect=post), \
                 mock.patch.object(sns_to_slack, '_already_posted', return_value=None), \
                 mock.patch.object(sns_to_slack, '_add_reaction',
                                   side_effect=lambda *args: reactions.append(args)):
                sns_to_slack._deliver_cloudwatch_bot(
                    'xoxb', alarm_fixture('OK'), 'acme-prod', 'us-east-1', '111', 'prod')
        except RuntimeError as exc:
            error = exc

        final = client.get_item(
            TableName=table,
            Key={'AlarmKey': {'S': alarm['AlarmArn']}},
            ConsistentRead=True,
        )['Item']
        return error, posted, reactions, final

    error, posted, reactions, final = _with_table(ok_after_abandoned_root)
    checks.extend([
        ("an OK breaks an expired ALARM_POSTING wedge", error is None),
        ("that fallback posts exactly one unthreaded recovery",
         len(posted) == 1 and posted[0].get('thread_ts') is None),
        ("an unthreaded recovery is not marked reply_broadcast",
         len(posted) == 1 and posted[0].get('reply_broadcast') is False),
        ("an unthreaded recovery does not react to a missing root", reactions == []),
        ("the abandoned-root recovery finalises the row",
         final.get('Status') == {'S': 'RESOLVED'}),
    ])

    # The lease must outlive the function's 30-second timeout but expire before Lambda's
    # first asynchronous retry at about 60 seconds. The boundary itself is reclaimable;
    # a strict comparison would waste that first retry.
    def lease_boundaries(client, table):
        with mock.patch.object(sns_to_slack.time, 'time', return_value=1000):
            sns_to_slack._claim_alarm_root('root', 100.0)
        with mock.patch.object(sns_to_slack.time, 'time', return_value=1044):
            try:
                sns_to_slack._claim_alarm_root('root', 100.0)
                root_before = 'claimed'
            except RuntimeError:
                root_before = 'held'
        with mock.patch.object(sns_to_slack.time, 'time', return_value=1045):
            try:
                root_at = sns_to_slack._claim_alarm_root('root', 100.0)
            except RuntimeError:
                root_at = None

        with mock.patch.object(sns_to_slack.time, 'time', return_value=900):
            root = sns_to_slack._claim_alarm_root('resolve', 100.0)
            sns_to_slack._finalize_alarm_root(
                'resolve', root, 'root-ts', 'start', 100.0)
        with mock.patch.object(sns_to_slack.time, 'time', return_value=1000):
            sns_to_slack._claim_resolution('resolve', 200.0)
        with mock.patch.object(sns_to_slack.time, 'time', return_value=1044):
            try:
                sns_to_slack._claim_resolution('resolve', 200.0)
                resolve_before = 'claimed'
            except RuntimeError:
                resolve_before = 'held'
        with mock.patch.object(sns_to_slack.time, 'time', return_value=1045):
            try:
                resolve_at = sns_to_slack._claim_resolution('resolve', 200.0)
            except RuntimeError:
                resolve_at = None
        return root_before, root_at, resolve_before, resolve_at

    root_before, root_at, resolve_before, resolve_at = _with_table(lease_boundaries)
    checks.extend([
        ("the claim lease is longer than timeout and shorter than first retry",
         30 < sns_to_slack.CLAIM_LEASE_SECONDS < 60),
        ("a root claim is still held one second before the boundary",
         root_before == 'held'),
        ("a root claim is reclaimable at the boundary", bool(root_at)),
        ("a resolve claim is still held one second before the boundary",
         resolve_before == 'held'),
        ("a resolve claim is reclaimable at the boundary", bool(resolve_at)),
    ])

    # TTL: a live incident must never inherit the tombstone's expiry.
    def ttl(client, table):
        first = sns_to_slack._claim_alarm_root('k', 100.0)
        sns_to_slack._finalize_alarm_root('k', first, 'root-1', 'start', 100.0)
        live = _row(client)
        ok = sns_to_slack._claim_resolution('k', 200.0)
        sns_to_slack._finalize_resolution('k', ok, 200.0)
        return live, _row(client)
    live, resolved = _with_table(ttl)
    checks.extend([
        ("a live ALARM row carries no TTL", 'ExpiresAt' not in live),
        ("a RESOLVED row carries one", 'ExpiresAt' in resolved),
        ("the resolved row records the state its watermark belongs to",
         resolved.get('LastEventState') == 'OK'),
    ])

    # Equal timestamps with CONFLICTING states are two truths about one instant and
    # nothing can order them, so whichever arrived second used to win. An ALARM could
    # reopen a row an OK had resolved at the same second, and the reverse arrival order
    # gave the opposite answer - the outcome depended purely on delivery race.
    def equal_conflict(client, table):
        first = sns_to_slack._claim_alarm_root('k', 100.0)
        sns_to_slack._finalize_alarm_root('k', first, 'root-1', 'start', 100.0)
        ok = sns_to_slack._claim_resolution('k', 200.0)
        sns_to_slack._finalize_resolution('k', ok, 200.0)
        return sns_to_slack._get_alarm_state('k')
    resolved_state = _with_table(equal_conflict)
    checks.extend([
        ("an equal-timestamp ALARM against a resolved OK is stale",
         sns_to_slack._is_stale(200.0, resolved_state, 'ALARM') is True),
        ("an equal-timestamp redelivery of the SAME state is not stale",
         sns_to_slack._is_stale(200.0, resolved_state, 'OK') is False),
        ("a newer ALARM after that resolve is not stale",
         sns_to_slack._is_stale(300.0, resolved_state, 'ALARM') is False),
    ])

    # The no-table configuration must be a no-op rather than an error, so the webhook
    # and tokened-but-tableless paths keep posting.
    with mock.patch.dict(os.environ, _FAKE_AWS_ENV), \
         mock.patch.object(sns_to_slack, 'SLACK_STATE_TABLE', ''):
        checks.extend([
            ("no table means no root claim", sns_to_slack._claim_alarm_root('k', 1.0) is None),
            ("no table means no resolve claim", sns_to_slack._claim_resolution('k', 1.0) is None),
            ("no table means no root finalise",
             sns_to_slack._finalize_alarm_root('k', 't', '1.1', 's', 1.0) is False),
            ("no table means no resolve finalise",
             sns_to_slack._finalize_resolution('k', 't', 1.0) is False),
            ("no table means no watermark touch",
             sns_to_slack._touch_alarm_watermark('k', '1.1', 1.0) is False),
        ])

    # The transition id has to distinguish this incident's card from the previous one's,
    # which is the whole reason the dedupe stopped matching on the alarm name.
    a1 = sns_to_slack._transition_id('arn:k', 'ALARM', '2026-08-01T12:00:00.000+0000')
    a2 = sns_to_slack._transition_id('arn:k', 'ALARM', '2026-08-01T13:00:00.000+0000')
    ok1 = sns_to_slack._transition_id('arn:k', 'OK', '2026-08-01T12:00:00.000+0000')
    checks.extend([
        ("two firings of one alarm get different transition ids", a1 != a2),
        ("the ALARM and OK of one instant differ", a1 != ok1),
        ("the same transition is stable across retries",
         a1 == sns_to_slack._transition_id('arn:k', 'ALARM',
                                           '2026-08-01T12:00:00.000+0000')),
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

    # -warning suffix: amber, no @channel. The description text already says "not
    # yet a full outage" - only the presentation layer was wrong before this fix.
    warning, _ = sns_to_slack.cloudwatch_card(
        alarm_fixture(alarm_name="acme-prod-nlb-healthy-hosts-warning"),
        "acme-prod", "us-east-1", "111", "prod")
    warning_text = str(warning)
    checks.extend([
        ("warning rail", warning["attachments"][0]["color"] == sns_to_slack.COLOR_WARNING),
        ("warning header", "🟠 WARNING · CloudWatch · acme-prod" in warning_text),
        ("warning does not page", "<!channel>" not in warning_text),
        ("warning footer says review", "Active · Review ·" in warning_text),
    ])

    # -critical suffix: unchanged from the pre-tiering behaviour.
    critical, _ = sns_to_slack.cloudwatch_card(
        alarm_fixture(alarm_name="acme-prod-nlb-healthy-hosts-critical"),
        "acme-prod", "us-east-1", "111", "prod")
    critical_text = str(critical)
    checks.extend([
        ("critical-suffix rail", critical["attachments"][0]["color"] == sns_to_slack.COLOR_CRITICAL),
        ("critical-suffix header", "🔴 CRITICAL · CloudWatch · acme-prod" in critical_text),
        ("critical-suffix pages channel", "<!channel>" in critical_text),
    ])

    # No suffix defaults to critical - the one unacceptable outcome of this change
    # would be silently downgrading an alarm outside the NLB healthy-hosts pair.
    unsuffixed, _ = sns_to_slack.cloudwatch_card(
        alarm_fixture(alarm_name="acme-prod-rds-cpu-usage"),
        "acme-prod", "us-east-1", "111", "prod")
    unsuffixed_text = str(unsuffixed)
    checks.extend([
        ("unsuffixed rail", unsuffixed["attachments"][0]["color"] == sns_to_slack.COLOR_CRITICAL),
        ("unsuffixed header", "🔴 CRITICAL · CloudWatch · acme-prod" in unsuffixed_text),
        ("unsuffixed pages channel", "<!channel>" in unsuffixed_text),
    ])

    # RESOLVED path for a -warning alarm is untouched: still green, still no mention.
    warning_resolved, _ = sns_to_slack.cloudwatch_card(
        alarm_fixture("OK", alarm_name="acme-prod-nlb-healthy-hosts-warning"),
        "acme-prod", "us-east-1", "111", "prod",
        started_at="2026-08-01T12:00:00.000+0000")
    warning_resolved_text = str(warning_resolved)
    checks.extend([
        ("warning-alarm resolve rail",
         warning_resolved["attachments"][0]["color"] == sns_to_slack.COLOR_RESOLVED),
        ("warning-alarm resolve header",
         "✅ RESOLVED · CloudWatch · acme-prod" in warning_resolved_text),
        ("warning-alarm resolve does not page", "<!channel>" not in warning_resolved_text),
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
              + lifecycle_checks() + ordering_checks() + dedupe_checks()
              + dynamo_checks())
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
