package validation

import "testing"

func TestDMSReadyStates(t *testing.T) {
	for _, s := range []string{"running", "load-complete"} {
		if !dmsReady(s) {
			t.Errorf("dmsReady(%q) = false, want true", s)
		}
	}
	// "starting" is the transient that caused the flake: it must NOT count as ready and
	// must NOT be fatal — it has to be waited out.
	for _, s := range []string{"starting", "ready", "creating", "modifying", "stopped"} {
		if dmsReady(s) {
			t.Errorf("dmsReady(%q) = true, want false", s)
		}
		if dmsTerminalBad(s) {
			t.Errorf("dmsTerminalBad(%q) = true — a run would fail on a recoverable state", s)
		}
	}
}

func TestDMSTerminalBadStates(t *testing.T) {
	for _, s := range []string{"failed", "failed-move", "deleting"} {
		if !dmsTerminalBad(s) {
			t.Errorf("dmsTerminalBad(%q) = false — the harness would wait out the full timeout for nothing", s)
		}
	}
}
