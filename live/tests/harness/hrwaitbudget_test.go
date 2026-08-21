package harness

import (
	"errors"
	"os"
	"strconv"
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
	t.Setenv(hrPodDeadlineEnv, "") // no pod deadline: isolate the override from the clamp
	os.Unsetenv(hrWaitBudgetOverrideEnv)
	if got, err := hrWaitBudget(hrWaitBudgetInstall); err != nil || got != hrWaitBudgetInstall {
		t.Errorf("hrWaitBudget(install) with no override = (%s, %v), want (%s, nil)", got, err, hrWaitBudgetInstall)
	}
	if got, err := hrWaitBudget(hrWaitBudgetUpgrade); err != nil || got != hrWaitBudgetUpgrade {
		t.Errorf("hrWaitBudget(upgrade) with no override = (%s, %v), want (%s, nil)", got, err, hrWaitBudgetUpgrade)
	}
}

func TestHRWaitBudgetValidOverrideReplacesBoth(t *testing.T) {
	t.Setenv(hrWaitBudgetOverrideEnv, "45s")
	t.Setenv(hrPodDeadlineEnv, "") // no pod deadline: isolate the override from the clamp
	if got, err := hrWaitBudget(hrWaitBudgetInstall); err != nil || got != 45*time.Second {
		t.Errorf("hrWaitBudget(install) with override = (%s, %v), want (45s, nil)", got, err)
	}
	if got, err := hrWaitBudget(hrWaitBudgetUpgrade); err != nil || got != 45*time.Second {
		t.Errorf("hrWaitBudget(upgrade) with override = (%s, %v), want (45s, nil)", got, err)
	}
}

func TestHRWaitBudgetInvalidOverrideIgnored(t *testing.T) {
	t.Setenv(hrWaitBudgetOverrideEnv, "not-a-duration")
	t.Setenv(hrPodDeadlineEnv, "") // no pod deadline: isolate the override from the clamp
	if got, err := hrWaitBudget(hrWaitBudgetInstall); err != nil || got != hrWaitBudgetInstall {
		t.Errorf("hrWaitBudget(install) with invalid override = (%s, %v), want the base %s unchanged, nil err", got, err, hrWaitBudgetInstall)
	}
	if got, err := hrWaitBudget(hrWaitBudgetUpgrade); err != nil || got != hrWaitBudgetUpgrade {
		t.Errorf("hrWaitBudget(upgrade) with invalid override = (%s, %v), want the base %s unchanged, nil err", got, err, hrWaitBudgetUpgrade)
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
			t.Setenv(hrPodDeadlineEnv, "") // no pod deadline: isolate the override from the clamp
			if got, err := hrWaitBudget(hrWaitBudgetInstall); err != nil || got != hrWaitBudgetInstall {
				t.Errorf("hrWaitBudget(install) with override %q = (%s, %v), want the base %s unchanged, nil err", v, got, err, hrWaitBudgetInstall)
			}
			if got, err := hrWaitBudget(hrWaitBudgetUpgrade); err != nil || got != hrWaitBudgetUpgrade {
				t.Errorf("hrWaitBudget(upgrade) with override %q = (%s, %v), want the base %s unchanged, nil err", v, got, err, hrWaitBudgetUpgrade)
			}
		})
	}
}

// TestClampHRWaitBudget pins clampHRWaitBudget's pure arithmetic — the runtime half of
// the consensus-panel fix (see phases.go's hrPodDeadlineEnv doc comment and
// deadline_invariant_test.go for the static half). elapsed is 0 in every case except
// where a case is explicitly about elapsed eating into the remaining budget.
//
// wantErr is checked only for non-nil-ness (errors.Is against errInsufficientPhaseTime
// is pinned separately, in TestClampHRWaitBudgetInsufficientReturnsNamedError) — this
// table is about the arithmetic, not the error identity.
func TestClampHRWaitBudget(t *testing.T) {
	cases := []struct {
		name        string
		desired     time.Duration
		deadlineEnv string
		elapsed     time.Duration
		wantBudget  time.Duration
		wantRule    string
		wantErr     bool
	}{
		{"unset deadline: no clamp", 90 * time.Minute, "", 0, 90 * time.Minute, "no-deadline", false},
		{"unparseable deadline: no clamp", 90 * time.Minute, "not-a-number", 0, 90 * time.Minute, "no-deadline", false},
		{"generous deadline: fits", 75 * time.Minute, "9000" /* 150m */, 0, 75 * time.Minute, "fits", false},
		{
			// 150m deadline, 90m desired, 25m reserve + 5m startup slack -> remaining = 150-0-25-5 = 120m >= 90m: fits.
			"exactly the invariant's own provision numbers: fits", hrWaitBudgetInstall, "9000", 0, hrWaitBudgetInstall, "fits", false,
		},
		{
			// 100m deadline, 90m desired, 25m reserve + 5m startup slack -> remaining = 100-0-25-5 = 70m < 90m: clamp to 70m.
			"tight deadline: clamped", 90 * time.Minute, "6000" /* 100m */, 0, 70 * time.Minute, "clamped", false,
		},
		{
			// 20m deadline, 90m desired, 25m reserve + 5m startup slack -> remaining = 20-0-25-5 = -10m, below the
			// 10m minimum: codex P1 fix means this is now "insufficient" (budget 0, error), never a raised floor.
			"deadline shorter than the reserve alone: insufficient, not floored", 90 * time.Minute, "1200" /* 20m */, 0, 0, "insufficient", true,
		},
		{
			// 120m deadline, 30m elapsed already, 90m desired, 25m reserve + 5m startup slack -> remaining = 120-30-25-5 = 60m < 90m: clamp to 60m.
			"elapsed time eats into the remaining budget", 90 * time.Minute, "7200" /* 120m */, 30 * time.Minute, 60 * time.Minute, "clamped", false,
		},
		{
			// remaining lands exactly AT the minimum (30m deadline - 0 elapsed - 30m reserve+slack = 0m... use a
			// deadline that puts remaining exactly at hrWaitBudgetMinimum: reserve+slack=30m, +10m minimum = 40m deadline.
			"remaining exactly at the minimum: allowed, clamped to it", 90 * time.Minute, "2400" /* 40m */, 0, 10 * time.Minute, "clamped", false,
		},
		{
			// One second under the minimum boundary: 40m deadline minus 1s.
			"remaining one second under the minimum: insufficient", 90 * time.Minute, "2399" /* 39m59s */, 0, 0, "insufficient", true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBudget, gotRule, err := clampHRWaitBudget(tc.desired, tc.deadlineEnv, tc.elapsed)
			if gotBudget != tc.wantBudget || gotRule != tc.wantRule || (err != nil) != tc.wantErr {
				t.Errorf("clampHRWaitBudget(%s, %q, %s) = (%s, %q, err=%v), want (%s, %q, err!=nil=%v)",
					tc.desired, tc.deadlineEnv, tc.elapsed, gotBudget, gotRule, err, tc.wantBudget, tc.wantRule, tc.wantErr)
			}
		})
	}
}

// TestClampHRWaitBudgetInsufficientReturnsNamedError pins the error identity and
// message shape codex asked for: a distinct, named error (errInsufficientPhaseTime,
// checkable via errors.Is) rather than an ad hoc string, carrying enough detail to
// explain the refusal in a pod log.
func TestClampHRWaitBudgetInsufficientReturnsNamedError(t *testing.T) {
	budget, rule, err := clampHRWaitBudget(90*time.Minute, "1200" /* 20m deadline */, 0)
	if err == nil {
		t.Fatal("expected a non-nil error when remaining is below hrWaitBudgetMinimum")
	}
	if !errors.Is(err, errInsufficientPhaseTime) {
		t.Errorf("error %v does not wrap errInsufficientPhaseTime", err)
	}
	if rule != "insufficient" {
		t.Errorf("rule = %q, want %q", rule, "insufficient")
	}
	if budget != 0 {
		t.Errorf("budget = %s, want 0 alongside a non-nil error — the caller must never start a wait when this errors", budget)
	}
}

// TestClampHRWaitBudgetNeverExceedsRemaining is the regression pin for the codex P1
// fix itself: the old floor RAISED a starved remaining value back up to a fixed
// minimum, handing the caller a budget bigger than what was actually left of the pod's
// deadline — Kubernetes then kills the pod mid-wait and the reserve meant for the
// dump/upload runs on nothing. The clamp must only ever shrink: whenever it returns a
// budget without an error, that budget can never exceed remaining; whenever remaining
// is below the minimum, it must error instead of manufacturing a budget.
func TestClampHRWaitBudgetNeverExceedsRemaining(t *testing.T) {
	cases := []struct {
		desired     time.Duration
		deadlineEnv string
		elapsed     time.Duration
	}{
		{90 * time.Minute, "6000", 0},                // 100m deadline: clamped case
		{90 * time.Minute, "1200", 0},                // 20m deadline: insufficient case
		{90 * time.Minute, "2400", 0},                // 40m deadline: remaining exactly at the minimum
		{90 * time.Minute, "7200", 30 * time.Minute}, // elapsed eating into remaining
		{5 * time.Minute, "2400", 0},                 // small desired: fits comfortably
		{200 * time.Hour, "9000", 0},                 // absurdly large desired: must clamp hard, never balloon
	}
	for _, tc := range cases {
		secs, convErr := strconv.Atoi(tc.deadlineEnv)
		if convErr != nil {
			t.Fatalf("bad test fixture deadlineEnv %q: %v", tc.deadlineEnv, convErr)
		}
		deadline := time.Duration(secs) * time.Second
		remaining := deadline - tc.elapsed - hrWaitReserve - hrPodStartupSlack

		got, rule, err := clampHRWaitBudget(tc.desired, tc.deadlineEnv, tc.elapsed)
		if err != nil {
			if got != 0 {
				t.Errorf("desired=%s deadlineEnv=%s elapsed=%s: insufficient path (rule=%s) returned a nonzero budget %s alongside its error",
					tc.desired, tc.deadlineEnv, tc.elapsed, rule, got)
			}
			if remaining >= hrWaitBudgetMinimum {
				t.Errorf("desired=%s deadlineEnv=%s elapsed=%s: returned an error (rule=%s) even though remaining %s >= minimum %s",
					tc.desired, tc.deadlineEnv, tc.elapsed, rule, remaining, hrWaitBudgetMinimum)
			}
			continue
		}
		if got > remaining {
			t.Errorf("desired=%s deadlineEnv=%s elapsed=%s: clamp returned %s (rule=%s), which EXCEEDS the %s actually remaining — this is the codex P1 bug (a floor raising a starved budget past the pod's real deadline)",
				tc.desired, tc.deadlineEnv, tc.elapsed, got, rule, remaining)
		}
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
// pinned at 0). So this asserts "clamped to approximately 70m, never more" rather than
// exact equality: the upper bound catches a clamp that silently stopped applying (==
// desired, which would be 90m here), the tolerance absorbs the real but bounded
// elapsed-time drift.
func TestHRWaitBudgetAppliesPodDeadlineClamp(t *testing.T) {
	t.Setenv(hrWaitBudgetOverrideEnv, "")
	os.Unsetenv(hrWaitBudgetOverrideEnv)
	t.Setenv(hrPodDeadlineEnv, "6000") // 100m: 90m desired install budget does not fit (100-25-5=70m remains)

	const want = 70 * time.Minute
	const tolerance = 10 * time.Second

	got, err := hrWaitBudget(hrWaitBudgetInstall)
	if err != nil {
		t.Fatalf("hrWaitBudget(install) under a 100m pod deadline returned an unexpected error: %v", err)
	}
	if got > want || got < want-tolerance {
		t.Errorf("hrWaitBudget(install) under a 100m pod deadline = %s, want ~70m (100m - 25m reserve - 5m startup slack, minus test elapsed time)", got)
	}

	t.Setenv(hrWaitBudgetOverrideEnv, "90m") // override equals the base, still doesn't fit under the same deadline
	got, err = hrWaitBudget(hrWaitBudgetUpgrade)
	if err != nil {
		t.Fatalf("hrWaitBudget(upgrade) with a 90m override under a 100m pod deadline returned an unexpected error: %v", err)
	}
	if got > want || got < want-tolerance {
		t.Errorf("hrWaitBudget(upgrade) with a 90m override under a 100m pod deadline = %s, want ~70m — the clamp must apply on top of the override", got)
	}
}

// TestHRWaitBudgetInsufficientReturnsErrorNotZeroWait pins the fix end to end through
// the real hrWaitBudget entry point: a pod deadline too tight even for the minimum must
// come back as a named error, never as a wait started with an unsafe (too-large or
// zero-but-silently-skipped) budget. Provision/Validate route this error through their
// normal validateStack-failure handling — see those call sites in phases.go.
func TestHRWaitBudgetInsufficientReturnsErrorNotZeroWait(t *testing.T) {
	t.Setenv(hrWaitBudgetOverrideEnv, "")
	os.Unsetenv(hrWaitBudgetOverrideEnv)
	t.Setenv(hrPodDeadlineEnv, "1200") // 20m: far short of the 25m+5m reserve alone

	got, err := hrWaitBudget(hrWaitBudgetInstall)
	if err == nil {
		t.Fatal("expected hrWaitBudget to return a non-nil error when the pod deadline leaves less than the minimum")
	}
	if !errors.Is(err, errInsufficientPhaseTime) {
		t.Errorf("error %v does not wrap errInsufficientPhaseTime", err)
	}
	if got != 0 {
		t.Errorf("hrWaitBudget returned budget %s alongside its error, want 0 — a non-zero budget here risks the caller starting a wait anyway", got)
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
