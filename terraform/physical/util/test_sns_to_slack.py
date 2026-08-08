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
    ("The replication, 'm3-gca', cannot scale down as the replication is already at the "
     "provided Minimum DMS Capacity Units, '2'.", "DROP", "the alert that started this"),
    ("DMS replication scaling up event.", "DROP", "routine autoscaling"),
    ("DMS replication scaling down event.", "DROP", "routine autoscaling"),
    ("DMS replication scaling event completed.", "DROP", "routine autoscaling"),

    # --- scale-blocked-at-MAX is deliberately NOT dropped: opposite meaning ---
    ("The replication, 'm3-gca', cannot scale up as the replication is already at the "
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
        "AlarmName": "3m-usac-api-5xx",
        "AlarmDescription": "Checkout API error rate is elevated. https://runbooks.example/api-5xx",
        "AlarmArn": "arn:aws:cloudwatch:us-east-1:111:alarm:3m-usac-api-5xx",
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


def deliver_cloudwatch(state, prior, reaction_error=None):
    """Run _deliver_cloudwatch_bot with every Slack and DynamoDB call captured."""
    updates, posts, states, reactions = [], [], [], []

    def _post(token, payload, thread_ts=None, reply_broadcast=False):
        posts.append({'payload': payload, 'thread_ts': thread_ts,
                      'broadcast': reply_broadcast})
        return '333.444'

    def _react(token, message_ts, name):
        reactions.append((message_ts, name))
        if reaction_error:
            raise RuntimeError(reaction_error)

    with mock.patch.object(sns_to_slack, '_get_alarm_state', return_value=prior), \
         mock.patch.object(sns_to_slack, '_update_bot',
                           side_effect=lambda token, ts, payload: updates.append((ts, payload))), \
         mock.patch.object(sns_to_slack, '_post_bot', side_effect=_post), \
         mock.patch.object(sns_to_slack, '_add_reaction', side_effect=_react), \
         mock.patch.object(sns_to_slack, '_put_alarm_state',
                           side_effect=lambda *args: states.append(args)):
        sns_to_slack._deliver_cloudwatch_bot(
            'xoxb', alarm_fixture(state), '3m-usac', 'us-east-1', '111', 'prod')
    return updates, posts, states, reactions


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

    # Substring collisions the word-bounded match must NOT read as critical. Each of these
    # contains error/fatal/fail as a substring but says the opposite.
    for message in ('Replication failover completed normally.',
                    'A nonfatal condition was observed and cleared.',
                    'The load completed error-free.'):
        posted, _ = run_dms_event(message, SERVERLESS_ARN)
        checks.append((f'"{message[:28]}" is not critical', posted == []))
    return checks


def lifecycle_checks():
    """The resolve semantics: the red root is written once and never rewritten."""
    checks = []
    firing = {"message_ts": "111.222", "started_at": "2026-08-01T12:00:00.000+0000",
              "status": "ALARM"}

    updates, posts, states, reactions = deliver_cloudwatch("OK", firing)
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
        ("resolution keeps idempotency tombstone",
         bool(states) and states[0][1] == "111.222" and states[0][-1] == "RESOLVED"),
    ])

    # reactions:write can be revoked, the root can be too old, Slack can 5xx. None of
    # that may cost us the resolution card, so the failure is swallowed at the call site
    # rather than allowed out of _deliver_cloudwatch_bot.
    try:
        updates, posts, states, reactions = deliver_cloudwatch(
            "OK", firing, reaction_error="missing_scope")
        escaped = False
    except Exception:  # noqa: BLE001 - the point of the check is that this cannot happen
        updates, posts, states, reactions = [], [], [], []
        escaped = True
    checks.extend([
        ("reaction failure does not escape the handler", not escaped),
        ("reaction failure still posts the resolution card", len(posts) == 1),
        ("reaction failure does not edit the root", updates == []),
        # If this raised, the tombstone would never be written and the Lambda retry
        # would post a second green card.
        ("reaction failure still writes the tombstone",
         bool(states) and states[0][-1] == "RESOLVED"),
    ])

    resolved = dict(firing, status="RESOLVED")
    updates, posts, states, reactions = deliver_cloudwatch("OK", resolved)
    checks.extend([
        ("duplicate OK posts no second card", posts == []),
        ("duplicate OK adds no second reaction", reactions == []),
        ("duplicate OK edits nothing", updates == []),
        ("duplicate OK rewrites the tombstone only",
         bool(states) and states[0][-1] == "RESOLVED"),
    ])

    # Repeat ALARM keeps editing: it is duplicate SNS delivery of the same firing event,
    # and the edit is what stops the same incident appearing twice. It never goes green.
    updates, posts, states, reactions = deliver_cloudwatch("ALARM", firing)
    checks.extend([
        ("repeat ALARM edits the root", len(updates) == 1 and updates[0][0] == "111.222"),
        ("repeat ALARM posts nothing new", posts == []),
        ("repeat ALARM adds no reaction", reactions == []),
        ("repeat ALARM stays ALARM", bool(states) and states[0][-1] == "ALARM"),
    ])

    updates, posts, states, reactions = deliver_cloudwatch("ALARM", None)
    checks.extend([
        ("first ALARM posts a root, unthreaded",
         len(posts) == 1 and posts[0]['thread_ts'] is None
         and posts[0]['broadcast'] is False),
        ("first ALARM edits nothing", updates == []),
    ])
    return checks


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
        alarm_fixture(), "3m-usac", "us-east-1", "111", "prod")
    active_text = str(active)
    checks.extend([
        ("critical rail", active["attachments"][0]["color"] == sns_to_slack.COLOR_CRITICAL),
        ("critical header", "🔴 CRITICAL · CloudWatch · 3m-usac" in active_text),
        ("critical UX sections", all(x in active_text for x in
                                     ("IMPACT", "SERVICE", "RESOURCE", "EVIDENCE"))),
        ("critical real actions", "Open CloudWatch" in active_text and "Runbook" in active_text),
        ("critical pages channel", "<!channel>" in active_text),
    ])

    resolved, _ = sns_to_slack.cloudwatch_card(
        alarm_fixture("OK"), "3m-usac", "us-east-1", "111", "prod",
        started_at="2026-08-01T12:00:00.000+0000")
    resolved_text = str(resolved)
    checks.extend([
        ("resolved rail", resolved["attachments"][0]["color"] == sns_to_slack.COLOR_RESOLVED),
        ("resolved header", "✅ RESOLVED · CloudWatch · 3m-usac" in resolved_text),
        ("resolved outcome", all(x in resolved_text for x in ("OUTCOME", "DURATION", "8m"))),
        ("resolved does not page", "<!channel>" not in resolved_text),
    ])

    dms_event = {"detail-type": "DMS Replication State Change",
                 "detail": {"detailMessage": "DMS replication has failed."}}
    dms = sns_to_slack.dms_card(
        dms_event, "3m-usac", "us-east-1", "111", "prod", "bi-replication",
        True, "https://console.aws.amazon.com/dms")
    dms_text = str(dms)
    checks.extend([
        ("DMS unified header", "🔴 CRITICAL · DMS · 3m-usac" in dms_text),
        ("DMS unified actions", "Open DMS" in dms_text),
    ])

    fallback = sns_to_slack.unknown_card(
        {"schema": "secret raw payload"}, "3m-usac", "us-east-1")
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
              + lifecycle_checks() + state_ttl_checks())
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
