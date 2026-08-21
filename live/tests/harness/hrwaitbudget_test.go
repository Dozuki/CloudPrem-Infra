package harness

import (
	"os"
	"testing"
	"time"
)

// hrWaitBudget backs the phase-aware ceiling passed to validateStack's Flux
// HelmRelease wait (Provision calls it with hrWaitBudgetInstall, Validate with
// hrWaitBudgetUpgrade — see phases.go). These tests pin the constants themselves and
// the HARNESS_HR_BUDGET_OVERRIDE parsing, independent of the two call sites.

func TestHRWaitBudgetConstants(t *testing.T) {
	if hrWaitBudgetInstall != 90*time.Minute {
		t.Errorf("hrWaitBudgetInstall = %s, want 90m (Provision phase)", hrWaitBudgetInstall)
	}
	if hrWaitBudgetUpgrade != 75*time.Minute {
		t.Errorf("hrWaitBudgetUpgrade = %s, want 75m (Validate phase)", hrWaitBudgetUpgrade)
	}
}

func TestHRWaitBudgetUnsetReturnsBase(t *testing.T) {
	t.Setenv(hrWaitBudgetOverrideEnv, "")
	os.Unsetenv(hrWaitBudgetOverrideEnv)
	if got := hrWaitBudget(hrWaitBudgetInstall); got != hrWaitBudgetInstall {
		t.Errorf("hrWaitBudget(install) with no override = %s, want %s", got, hrWaitBudgetInstall)
	}
	if got := hrWaitBudget(hrWaitBudgetUpgrade); got != hrWaitBudgetUpgrade {
		t.Errorf("hrWaitBudget(upgrade) with no override = %s, want %s", got, hrWaitBudgetUpgrade)
	}
}

func TestHRWaitBudgetValidOverrideReplacesBoth(t *testing.T) {
	t.Setenv(hrWaitBudgetOverrideEnv, "45s")
	if got := hrWaitBudget(hrWaitBudgetInstall); got != 45*time.Second {
		t.Errorf("hrWaitBudget(install) with override = %s, want 45s", got)
	}
	if got := hrWaitBudget(hrWaitBudgetUpgrade); got != 45*time.Second {
		t.Errorf("hrWaitBudget(upgrade) with override = %s, want 45s", got)
	}
}

func TestHRWaitBudgetInvalidOverrideIgnored(t *testing.T) {
	t.Setenv(hrWaitBudgetOverrideEnv, "not-a-duration")
	if got := hrWaitBudget(hrWaitBudgetInstall); got != hrWaitBudgetInstall {
		t.Errorf("hrWaitBudget(install) with invalid override = %s, want the base %s unchanged", got, hrWaitBudgetInstall)
	}
	if got := hrWaitBudget(hrWaitBudgetUpgrade); got != hrWaitBudgetUpgrade {
		t.Errorf("hrWaitBudget(upgrade) with invalid override = %s, want the base %s unchanged", got, hrWaitBudgetUpgrade)
	}
}

// TestHRWaitBudgetZeroOrNegativeOverrideRejected pins the codex MINOR fix:
// time.ParseDuration happily accepts "-5m" and "0s" (no error), but either one would
// make AwaitHelmReleaseReady time out on its first iteration, indistinguishable from a
// genuinely broken install. hrWaitBudget must reject them the same way it rejects an
// unparseable string: warn and fall back to base.
func TestHRWaitBudgetZeroOrNegativeOverrideRejected(t *testing.T) {
	for _, v := range []string{"0s", "0m", "-5m", "-1ns"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(hrWaitBudgetOverrideEnv, v)
			if got := hrWaitBudget(hrWaitBudgetInstall); got != hrWaitBudgetInstall {
				t.Errorf("hrWaitBudget(install) with override %q = %s, want the base %s unchanged", v, got, hrWaitBudgetInstall)
			}
			if got := hrWaitBudget(hrWaitBudgetUpgrade); got != hrWaitBudgetUpgrade {
				t.Errorf("hrWaitBudget(upgrade) with override %q = %s, want the base %s unchanged", v, got, hrWaitBudgetUpgrade)
			}
		})
	}
}

func TestFormatMinutes(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{90 * time.Minute, "90m"},
		{75 * time.Minute, "75m"},
		{45 * time.Second, "45s"},
		{500 * time.Millisecond, "500ms"},
	}
	for _, tc := range cases {
		if got := formatMinutes(tc.d); got != tc.want {
			t.Errorf("formatMinutes(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
