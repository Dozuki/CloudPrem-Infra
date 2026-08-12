package validation

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// condition's observedGeneration return is what keeps a stale Released=False from being
// blamed on a newer spec attempt. Getting it wrong is silent in both directions: decoding
// a real generation as absent suppresses every failure report, and reporting 0 where there
// is no data would attribute an old error to a new generation. So pin the decode
// explicitly, including the "absent" case, which must come back as -1 so the caller can
// route it to the unscoped bucket rather than claiming it for the current generation.
func TestConditionObservedGeneration(t *testing.T) {
	hrWith := func(cond map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []interface{}{cond},
			},
		}}
	}

	cases := []struct {
		name    string
		cond    map[string]interface{}
		wantGen int64
	}{
		{
			name:    "int64 as the dynamic client decodes it",
			cond:    map[string]interface{}{"type": "Released", "status": "False", "reason": "UpgradeFailed", "message": "hook failed", "observedGeneration": int64(7)},
			wantGen: 7,
		},
		{
			name:    "float64 from a different decode path",
			cond:    map[string]interface{}{"type": "Released", "status": "False", "reason": "UpgradeFailed", "observedGeneration": float64(7)},
			wantGen: 7,
		},
		{
			name:    "absent observedGeneration reads as unknown, not zero",
			cond:    map[string]interface{}{"type": "Released", "status": "False", "reason": "UpgradeFailed"},
			wantGen: -1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reason, _, gen := condition(hrWith(tc.cond), "Released")
			if status != "False" || reason != "UpgradeFailed" {
				t.Fatalf("status/reason = %q/%q, want False/UpgradeFailed", status, reason)
			}
			if gen != tc.wantGen {
				t.Fatalf("observedGeneration = %d, want %d", gen, tc.wantGen)
			}
		})
	}

	t.Run("missing condition returns unknown generation", func(t *testing.T) {
		status, _, _, gen := condition(hrWith(map[string]interface{}{"type": "Ready", "status": "True"}), "Released")
		if status != "" {
			t.Fatalf("status = %q, want empty for an absent condition", status)
		}
		if gen != -1 {
			t.Fatalf("observedGeneration = %d, want -1 for an absent condition", gen)
		}
	})

	t.Run("no status block at all", func(t *testing.T) {
		status, _, _, gen := condition(&unstructured.Unstructured{Object: map[string]interface{}{}}, "Released")
		if status != "" || gen != -1 {
			t.Fatalf("status/gen = %q/%d, want empty/-1", status, gen)
		}
	})
}

// isTerminalReason must keep UpgradeFailed: it is unreachable on a release running
// RetryOnFailure, but a pre-v9.2.2 baseline still reports a dead upgrade that way and the
// harness runs upgrade tests from those baselines.
func TestIsTerminalReason(t *testing.T) {
	for _, r := range []string{"InstallFailed", "UpgradeFailed", "TestFailed", "RollbackFailed", "UninstallFailed", "ChartPullFailed"} {
		if !isTerminalReason(r) {
			t.Errorf("isTerminalReason(%q) = false, want true", r)
		}
	}
	for _, r := range []string{"Progressing", "ArtifactFailed", "ReconciliationSucceeded", ""} {
		if isTerminalReason(r) {
			t.Errorf("isTerminalReason(%q) = true, want false", r)
		}
	}
}
