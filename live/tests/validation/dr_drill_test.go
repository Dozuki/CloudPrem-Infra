package validation

import (
	"errors"
	"testing"
)

func TestClusterIDFromArn(t *testing.T) {
	got := clusterIDFromArn("arn:aws:rds:us-west-2:076248559428:cluster:smoke-full-dr")
	if got != "smoke-full-dr" {
		t.Errorf("clusterIDFromArn = %q, want smoke-full-dr", got)
	}
}

func TestShortRunID(t *testing.T) {
	// The realistic shape: the numeric timestamp is the unique part.
	if got := shortRunID("local-1785230883-fresh-full"); got != "1785230883" {
		t.Errorf("shortRunID = %q, want the timestamp", got)
	}
	// A run id with no numeric part must still yield something RDS-identifier-safe.
	got := shortRunID("Custom_Run.Name")
	if got == "" || len(got) > 12 {
		t.Errorf("shortRunID fallback = %q, want non-empty <=12 chars", got)
	}
	for _, r := range got {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			t.Errorf("shortRunID fallback %q contains invalid rune %q", got, r)
		}
	}
}

func TestCombineErrs(t *testing.T) {
	a, b := errors.New("verify failed"), errors.New("cleanup failed")
	// The verification error must win the %w slot so callers can unwrap the real cause;
	// cleanup trouble rides along as text.
	c := combineErrs(a, b)
	if !errors.Is(c, a) {
		t.Error("combined error does not unwrap to the verification error")
	}
	if combineErrs(nil, b) != b || combineErrs(a, nil) != a {
		t.Error("nil handling wrong")
	}
	if combineErrs(nil, nil) != nil {
		t.Error("nil+nil should be nil")
	}
}
