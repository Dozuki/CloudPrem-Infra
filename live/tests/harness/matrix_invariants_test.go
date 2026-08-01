package harness

import "testing"

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
