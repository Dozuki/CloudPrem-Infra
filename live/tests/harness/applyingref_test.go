package harness

import (
	"context"
	"testing"
)

// hftj2Manifest is the real smoke1bc2 manifest as it stood after the run was superseded
// mid-apply: scenario upgrade, baseline v9.6.0, target 4ef62211, and applied_ref EMPTY
// because Provision only wrote it after an apply that never returned.
func hftj2Manifest() *RunManifest {
	return &RunManifest{
		Scenario: "upgrade", ConfigName: "bi_ha",
		FromRef: "v9.6.0", ToRef: "4ef62211bde058785c139c22b8f172ae513b8db5",
		DeleteAfter: "2026-08-18T01:20:08Z",
	}
}

// Without ApplyingRef, every ref resolution for a killed run falls through to ToRef -
// so the teardown of a BASELINE-built stack runs TARGET code. That is what happened to
// harness-bi-ha-hftj2 (bd Lodestar-1xm.36).
func TestKilledMidApplyResolvesToTheRefThatWasApplying(t *testing.T) {
	rm := hftj2Manifest()
	rm.ApplyingRef, rm.ApplyingSide = "v9.6.0", string(SideBaseline)

	if got := TeardownRepoRef(rm); got != "v9.6.0" {
		t.Errorf("TeardownRepoRef = %q, want the baseline ref the apply was running with", got)
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide: %v", err)
	}
	if ref != "v9.6.0" || side != SideBaseline {
		t.Errorf("teardown would destroy against (%s, %s), want (v9.6.0, baseline)", ref, side)
	}
}

// A completed apply still wins: ApplyingRef is the fallback, never an override.
func TestAppliedRefStillBeatsApplyingRef(t *testing.T) {
	rm := hftj2Manifest()
	rm.ApplyingRef, rm.ApplyingSide = "v9.6.0", string(SideBaseline)
	rm.AppliedRef, rm.AppliedSide = rm.ToRef, string(SideTarget)

	if got := TeardownRepoRef(rm); got != rm.ToRef {
		t.Errorf("TeardownRepoRef = %q, want the completed apply's ref", got)
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide: %v", err)
	}
	if ref != rm.ToRef || side != SideTarget {
		t.Errorf("teardown resolved (%s, %s), want the completed apply's (ref, side)", ref, side)
	}
}

// A manifest predating both fields keeps its old behaviour.
func TestPreApplyingRefManifestStillFallsBackToToRef(t *testing.T) {
	rm := hftj2Manifest()
	if got := TeardownRepoRef(rm); got != rm.ToRef {
		t.Errorf("TeardownRepoRef = %q, want the ToRef fallback for a manifest with neither field", got)
	}
}

// Validate's precondition must keep meaning "the target apply SUCCEEDED". An upgrade
// that started and died leaves ApplyingRef == ToRef; if that satisfied the precondition,
// Validate would render the target side against a half-applied stack.
func TestApplyingRefDoesNotSatisfyValidatePrecondition(t *testing.T) {
	rm := hftj2Manifest()
	rm.ApplyingRef, rm.ApplyingSide = rm.ToRef, string(SideTarget)
	if err := validatePreconditions(rm); err == nil {
		t.Error("validatePreconditions accepted a manifest where the target apply only STARTED; it must require a completed apply")
	}
}

// Provision must persist the ref it is about to apply before it applies it.
func TestProvisionRecordsApplyingRefBeforeApplying(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	m := &Matrix{Configs: []Config{{Name: "bi_ha", Env: "bi"}}}
	p := PhaseParams{Matrix: m, Store: store, ConfigName: "bi_ha", RunID: "hftj2", RepoDir: t.TempDir()}
	cfg, err := p.Config()
	if err != nil {
		t.Fatal(err)
	}
	// RepoDir is not a git repo, so Provision dies at the worktree step - a deterministic
	// stand-in for a run killed before its apply returns.
	_ = p.Provision(ctx, "upgrade", "v9.6.0", "v9.7.0", "2026-08-18T01:20:08Z", "dozuki")

	rm, ok, lerr := store.Load(ctx, p.statePrefix(cfg))
	if lerr != nil || !ok {
		t.Fatalf("manifest not persisted: ok=%v err=%v", ok, lerr)
	}
	if rm.ApplyingRef != "v9.6.0" || rm.ApplyingSide != string(SideBaseline) {
		t.Errorf("applying_ref/side = %q/%q, want v9.6.0/baseline (an upgrade scenario applies the BASELINE first)", rm.ApplyingRef, rm.ApplyingSide)
	}
	if rm.AppliedRef != "" {
		t.Errorf("applied_ref = %q, want empty: no apply completed", rm.AppliedRef)
	}
}
