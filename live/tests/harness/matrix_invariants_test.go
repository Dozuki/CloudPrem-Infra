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
