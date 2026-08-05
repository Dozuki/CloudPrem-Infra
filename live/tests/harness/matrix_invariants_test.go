package harness

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Guards the real matrix.yaml, not testdata/. Everything else in this package
// tests against the fixture, which means a new config in the production matrix
// gets no coverage at all until it runs on live AWS.
//
// alertmanager_slack_enabled defaults to true in terraform/logical, so a config
// that omits it inherits the fleet Slack route and pages the shared channel for
// a cluster that exists for an hour. Worse, teardown deletes Alertmanager with
// the cluster, so the resolved notification never sends and the firing message
// outlives the stack. Every smoke stack must opt out, and a comment on one
// config is not enforcement.
func TestAllMatrixConfigsDisableAlertmanagerSlack(t *testing.T) {
	m, err := LoadMatrix("../matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	if len(m.Configs) == 0 {
		t.Fatal("matrix has no configs; the guard below would pass vacuously")
	}
	for _, c := range m.Configs {
		v, ok := c.FeatureFlags["alertmanager_slack_enabled"]
		if !ok {
			t.Errorf("config %q does not set alertmanager_slack_enabled; it will default to true and page the fleet Slack channel", c.Name)
			continue
		}
		if v != false {
			t.Errorf("config %q has alertmanager_slack_enabled = %v, want false", c.Name, v)
		}
	}
}

// TestPostStatusUsesEvidenceSubcommand guards the YAML/Go coupling: nothing else
// keeps argo/20-matrix.yaml's post-status script pointed at `harness evidence` and
// the relay payload carrying log_excerpt. If someone renames or drops the
// subcommand on either side, this is what catches it - the card would otherwise
// silently degrade to today's node summary with no test failure anywhere.
func TestPostStatusUsesEvidenceSubcommand(t *testing.T) {
	b, err := os.ReadFile("../argo/20-matrix.yaml")
	if err != nil {
		t.Fatalf("read 20-matrix.yaml: %v", err)
	}
	script := string(b)

	if !strings.Contains(script, "harness evidence") {
		t.Error("post-status no longer calls `harness evidence`")
	}
	if !strings.Contains(script, `select(.type == "Pod")`) {
		t.Error("post-status dropped the jq fallback expression (requirement 3: evidence must never be the only path)")
	}
	if !strings.Contains(script, "log_excerpt") {
		t.Error("post-status's relay payload no longer carries log_excerpt")
	}
}

// TestMatrixConfigsSaltProducesUniqueCustomer guards Config.Salted's documented no-op
// path (config.go): a base customer already at or past the 10-char terraform cap salts
// to itself. The janitor (harness/janitor.go) already refuses to reason about a candidate
// whose salted customer did not change - it reports needs-review rather than guess - but a
// non-unique identifier is a real collision risk for whatever else derives IAM/Vault/k8s
// resource names from it, not just the janitor. Every config's customer must leave Salted
// room to mutate it.
func TestMatrixConfigsSaltProducesUniqueCustomer(t *testing.T) {
	m, err := LoadMatrix("../matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	for _, c := range m.Configs {
		customer, ok := c.FeatureFlags["customer"].(string)
		if !ok || customer == "" {
			t.Errorf("config %q has no (or a non-string) customer feature flag; the janitor cannot derive an identity for it", c.Name)
			continue
		}
		salted := c.Salted("janitor-invariant-probe")
		got, _ := salted.FeatureFlags["customer"].(string)
		if got == customer {
			t.Errorf("config %q: customer %q is at/past the 10-char salt cap (Salted is a no-op on it); two runs of this config would collide on the same resource identifier", c.Name, customer)
		}
	}
}

// TestJanitorCronMatchesTheGoConstants guards the two Go/YAML couplings the janitor
// depends on and that nothing else enforces. Both are "copy this literal by hand"
// contracts documented in comments on either side, which is exactly the kind of
// agreement that rots silently.
//
//   - schema_version: the notify script asserts the report's version before running its
//     jq pipeline over the field names. If Go bumps JanitorReportSchemaVersion and the
//     script keeps asserting the old number, EVERY cycle posts "cannot read this
//     cycle's report" instead of a report - loud, but permanently wrong.
//   - activeDeadlineSeconds: JanitorPodActiveDeadlineSeconds is the source of truth
//     Sweep's wall-clock budget is derived from. If the pod's real deadline is shorter
//     than the constant, Sweep budgets time the pod does not have and a destroy gets
//     SIGKILLed mid-run - the one failure the budget exists to prevent.
func TestJanitorCronMatchesTheGoConstants(t *testing.T) {
	b, err := os.ReadFile("../argo/50-janitor-cron.yaml")
	if err != nil {
		t.Fatalf("read 50-janitor-cron.yaml: %v", err)
	}
	yaml := string(b)

	wantSchema := fmt.Sprintf(`[ "$SCHEMA_VERSION" != "%d" ]`, JanitorReportSchemaVersion)
	if !strings.Contains(yaml, wantSchema) {
		t.Errorf("the notify script does not assert schema_version %d (looked for %s); Go's JanitorReportSchemaVersion and the script's literal have drifted apart",
			JanitorReportSchemaVersion, wantSchema)
	}

	wantDeadline := fmt.Sprintf("activeDeadlineSeconds: %d", JanitorPodActiveDeadlineSeconds)
	if !strings.Contains(yaml, wantDeadline) {
		t.Errorf("the scan pod does not carry %q; JanitorPodActiveDeadlineSeconds (%d) is the number Sweep's budget is derived from and the pod must actually get that long",
			wantDeadline, JanitorPodActiveDeadlineSeconds)
	}

	// The selector contract, in the other direction: these are the exact state strings
	// harness/janitor.go emits, and a rename on the Go side that missed the script
	// would silently drop a whole class of candidate out of Slack.
	for _, state := range []CandidateState{StateOrphan, StateBlocked, StateResidue, StateUnknown, StateNeedsReview} {
		if !strings.Contains(yaml, fmt.Sprintf(`.state=="%s"`, state)) {
			t.Errorf("the notify script's selectors never mention state %q; candidates in that state would be invisible in Slack", state)
		}
	}
	if !strings.Contains(yaml, `(.sweep_result // "") != "destroyed"`) {
		t.Error(`the notify script no longer excludes sweep_result "destroyed" from the alarm set; a clean destroy would render under a "teardown FAILED" headline (Sweep leaves State=orphan on purpose)`)
	}
}

// TestSupersedeStepInvariants locks the shape of the supersede-older step in
// argo/20-matrix.yaml. Each assertion is a property the consensus review of the
// change identified as load-bearing; losing any of them silently reintroduces a
// failure mode that was observed live.
func TestSupersedeStepInvariants(t *testing.T) {
	b, err := os.ReadFile("../argo/20-matrix.yaml")
	if err != nil {
		t.Fatalf("read 20-matrix.yaml: %v", err)
	}
	y := string(b)

	// The step must exist and must run before the fan-out. Compare the step
	// entries (template: refs), not "name:" strings - a scenario PARAMETER
	// definition appears earlier in the file.
	supersede := strings.Index(y, "template: supersede-older")
	fanout := strings.Index(y, "template: submit-child")
	if supersede == -1 {
		t.Fatal("supersede step is gone from the matrix")
	}
	if fanout != -1 && supersede > fanout {
		t.Error("supersede no longer runs before the scenario fan-out")
	}

	// Pod-level failures (deadline kill, image pull, eviction) must never block
	// the fan-out: the step is capacity hygiene, not a gate.
	if !strings.Contains(y, "continueOn:") {
		t.Error("supersede lost its continueOn; a pod-level failure would block the fan-out and post a false failure to the PR head")
	}

	// Graceful stop only: Terminate skips the teardown exit handler and would
	// leak live stacks.
	if strings.Contains(y, `"shutdown":"Terminate"`) || strings.Contains(y, "shutdown: Terminate") {
		t.Error("supersede uses Terminate; teardown exit handlers would be skipped")
	}
	if !strings.Contains(y, `"shutdown":"Stop"`) {
		t.Error("supersede no longer patches shutdown: Stop")
	}

	// The superseded marker must ride the same patch as the stop, and identity
	// must come from the label contract, never name parsing.
	if !strings.Contains(y, `"harness/superseded":"true"`) {
		t.Error("supersede no longer sets the harness/superseded marker; post-status would post false failures for stopped runs")
	}
	if !strings.Contains(y, "harness/trigger=pr,harness/pr=") {
		t.Error("supersede no longer selects by the harness/pr label contract")
	}
	if !strings.Contains(y, `.metadata.name != $self`) {
		t.Error("supersede lost its self-exclusion")
	}

	// Every kubectl call is bounded; a hung API server must not eat the
	// activeDeadline and fail the pod.
	if !strings.Contains(y, "--request-timeout=10s") {
		t.Error("supersede's kubectl calls lost their request timeout")
	}

	// post-status must suppress notifications for superseded runs.
	if !strings.Contains(y, "harness/superseded") || !strings.Contains(y, "superseded run; skipping") {
		t.Error("post-status no longer suppresses notifications for superseded runs")
	}
}
