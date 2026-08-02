"""
Fixture matrix for the DMS routing in sns_to_slack.py.

Run: python3 util/test_sns_to_slack.py   (no deps, no pytest, exits non-zero on failure)

This exists because the routing is substring matching against AWS's own event wording, and
two of the phrases in DMS_ROUTINE_MESSAGES are prefixes of messages that MUST page:

    "provisioning its capacity"  is inside  "deprovisioning its capacity"
    "has been provisioned"       is inside  "has been deprovisioned"

Only the order of the checks keeps those alerts alive - the failure tokens are evaluated
before the denylist is consulted. That is invisible at the call site and trivially broken by
someone "tidying up" the branch, hence a test rather than a comment.

Messages below are AWS's documented wording for both source types (Replication and
ReplicationTask, from the DMS user guide's EventBridge event tables) plus the scale-block
variants observed live, which are not in the published tables.

The module is loaded by path rather than imported so this file does not depend on the
package layout of whatever directory terraform zips it into.
"""

import os
import sys
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


def classify(detail_message, detail_type="DMS Replication State Change"):
    """Mirror of the routing in lambda_handler's detail-type branch.

    Kept in step with sns_to_slack.py by hand. If the handler's logic changes, this must
    change with it - the point of the fixtures below is the message-to-outcome mapping,
    not this three-line reimplementation.
    """
    haystack = f"{detail_message} {detail_type}".lower()
    critical = ("ERROR" in detail_message or "FATAL" in detail_message
                or "fail" in haystack or "deprovision" in haystack)
    if not critical and any(p in haystack for p in DROP):
        return "DROP"
    return "PAGE" if critical else "POST"


# (message, expected outcome, why it matters)
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

    updates, posts, states = [], [], []
    prior = {"message_ts": "111.222", "started_at": "2026-08-01T12:00:00.000+0000",
             "status": "ALARM"}
    with mock.patch.object(sns_to_slack, "_get_alarm_state", return_value=prior), \
         mock.patch.object(sns_to_slack, "_update_bot",
                           side_effect=lambda token, ts, payload: updates.append((ts, payload))), \
         mock.patch.object(sns_to_slack, "_post_bot",
                           side_effect=lambda token, payload, thread_ts=None:
                           posts.append((payload, thread_ts)) or "333.444"), \
         mock.patch.object(sns_to_slack, "_put_alarm_state",
                           side_effect=lambda *args: states.append(args)):
        sns_to_slack._deliver_cloudwatch_bot(
            "xoxb", alarm_fixture("OK"), "3m-usac", "us-east-1", "111", "prod")
    checks.extend([
        ("resolution updates root", len(updates) == 1 and updates[0][0] == "111.222"),
        ("resolution posts thread event", len(posts) == 1 and posts[0][1] == "111.222"),
        ("resolution keeps idempotency tombstone", bool(states) and states[0][-1] == "RESOLVED"),
    ])
    return checks


def main():
    failures = []
    for message, expected, why in CASES:
        got = classify(message)
        if got != expected:
            failures.append((message, expected, got, why))

    checks = renderer_checks()
    for name, passed in checks:
        if not passed:
            failures.append((name, "PASS", "FAIL", "renderer/lifecycle contract"))

    for message, expected, got, why in failures:
        print(f"FAIL  expected {expected}, got {got}\n      {message[:100]}\n      ({why})")

    total = len(CASES) + len(checks)
    print(f"\n{total - len(failures)}/{total} passed")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
