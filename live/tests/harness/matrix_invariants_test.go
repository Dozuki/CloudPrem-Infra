package harness

import (
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
