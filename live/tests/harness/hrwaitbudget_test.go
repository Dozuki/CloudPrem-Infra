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

// TestClampHRWaitBudget pins clampHRWaitBudget's pure arithmetic — the runtime half of
// the consensus-panel fix (see phases.go's hrPodDeadlineEnv doc comment and
// deadline_invariant_test.go for the static half). elapsed is 0 in every case except
// where a case is explicitly about elapsed eating into the remaining budget.
func TestClampHRWaitBudget(t *testing.T) {
	cases := []struct {
		name        string
		desired     time.Duration
		deadlineEnv string
		elapsed     time.Duration
		wantBudget  time.Duration
		wantRule    string
	}{
		{"unset deadline: no clamp", 90 * time.Minute, "", 0, 90 * time.Minute, "no-deadline"},
		{"unparseable deadline: no clamp", 90 * time.Minute, "not-a-number", 0, 90 * time.Minute, "no-deadline"},
		{"generous deadline: fits", 75 * time.Minute, "9000" /* 150m */, 0, 75 * time.Minute, "fits"},
		{
			// 150m deadline, 90m desired, 25m reserve -> remaining = 150-0-25 = 125m >= 90m: fits.
			"exactly the invariant's own provision numbers: fits", hrWaitBudgetInstall, "9000", 0, hrWaitBudgetInstall, "fits",
		},
		{
			// 100m deadline, 90m desired, 25m reserve -> remaining = 100-0-25 = 75m < 90m: clamp to 75m.
			"tight deadline: clamped", 90 * time.Minute, "6000" /* 100m */, 0, 75 * time.Minute, "clamped",
		},
		{
			// 20m deadline, 90m desired, 25m reserve -> remaining = 20-0-25 = -5m < floor (10m): floored.
			"deadline shorter than the reserve alone: floored", 90 * time.Minute, "1200" /* 20m */, 0, hrWaitBudgetFloor, "floored",
		},
		{
			// 120m deadline, 30m elapsed already, 90m desired, 25m reserve -> remaining = 120-30-25 = 65m < 90m: clamp to 65m.
			"elapsed time eats into the remaining budget", 90 * time.Minute, "7200" /* 120m */, 30 * time.Minute, 65 * time.Minute, "clamped",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBudget, gotRule := clampHRWaitBudget(tc.desired, tc.deadlineEnv, tc.elapsed)
			if gotBudget != tc.wantBudget || gotRule != tc.wantRule {
				t.Errorf("clampHRWaitBudget(%s, %q, %s) = (%s, %q), want (%s, %q)",
					tc.desired, tc.deadlineEnv, tc.elapsed, gotBudget, gotRule, tc.wantBudget, tc.wantRule)
			}
		})
	}
}

// TestHRWaitBudgetAppliesPodDeadlineClamp exercises the clamp through the real
// hrWaitBudget entry point (env var, not the pure function directly), and pins that the
// clamp applies ON TOP of an active HARNESS_HR_BUDGET_OVERRIDE — an override cannot push
// the wait past the pod's own deadline any more than the base budget can.
//
// hrWaitBudget's elapsed comes from time.Since(processStart), which is a few
// milliseconds-to-seconds by the time this test runs (real wall-clock, not injectable —
// clampHRWaitBudget's own table test above covers the exact arithmetic with elapsed
// pinned at 0). So this asserts "clamped to approximately 75m, never more" rather than
// exact equality: allBelow catches a clamp that silently stopped applying (== desired,
// which would be 90m here), tolerance catches the real but bounded elapsed-time drift.
func TestHRWaitBudgetAppliesPodDeadlineClamp(t *testing.T) {
	t.Setenv(hrWaitBudgetOverrideEnv, "")
	os.Unsetenv(hrWaitBudgetOverrideEnv)
	t.Setenv(hrPodDeadlineEnv, "6000") // 100m: 90m desired install budget does not fit (100-25=75m remains)

	const want = 75 * time.Minute
	const tolerance = 10 * time.Second

	got := hrWaitBudget(hrWaitBudgetInstall)
	if got > want || got < want-tolerance {
		t.Errorf("hrWaitBudget(install) under a 100m pod deadline = %s, want ~75m (100m - 25m reserve, minus test elapsed time)", got)
	}

	t.Setenv(hrWaitBudgetOverrideEnv, "90m") // override equals the base, still doesn't fit under the same deadline
	got = hrWaitBudget(hrWaitBudgetUpgrade)
	if got > want || got < want-tolerance {
		t.Errorf("hrWaitBudget(upgrade) with a 90m override under a 100m pod deadline = %s, want ~75m — the clamp must apply on top of the override", got)
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
