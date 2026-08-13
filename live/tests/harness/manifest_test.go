package harness

import (
	"context"
	"strings"
	"testing"
)

func TestMemStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	if _, ok, err := s.Load(ctx, "run1-min/"); err != nil || ok {
		t.Fatalf("empty load: got ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	want := &RunManifest{
		Scenario: "upgrade", ConfigName: "min_default",
		FromRef: "v6.0.3", ToRef: "v6.1-release",
		DeleteAfter: "2026-06-25T00:00:00Z", AppliedRef: "v6.0.3",
		BaselineRev: 0, Namespace: "dozuki", AccountID: "076000000000",
		Region: "us-east-1", DRRegion: "us-west-2",
	}
	if err := s.Save(ctx, "run1-min/", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := s.Load(ctx, "run1-min/")
	if err != nil || !ok {
		t.Fatalf("load after save: ok=%v err=%v", ok, err)
	}
	if got.ToRef != "v6.1-release" || got.AppliedRef != "v6.0.3" || got.Scenario != "upgrade" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

// TestManifestRoundTripAppliedCustomerAndKeepOnFailure covers the two fields added for
// janitor defects P1/P2 - written and read back exactly like every other field.
func TestManifestRoundTripAppliedCustomerAndKeepOnFailure(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	want := &RunManifest{
		ConfigName: "min_default", DeleteAfter: "2026-06-25T00:00:00Z",
		AppliedCustomer:       "smokeab12",
		KeepOnFailure:         true,
		KeepOnFailureRecorded: true,
	}
	if err := s.Save(ctx, "run1-min/", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := s.Load(ctx, "run1-min/")
	if err != nil || !ok {
		t.Fatalf("load after save: ok=%v err=%v", ok, err)
	}
	if got.AppliedCustomer != "smokeab12" || !got.KeepOnFailure || !got.KeepOnFailureRecorded {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

// TestManifestBackwardCompatMissingNewFields is the explicit regression test for the
// requirement that adding AppliedCustomer/KeepOnFailure/KeepOnFailureRecorded must stay
// backward compatible: a manifest JSON blob written before these fields existed (no
// keys at all for them) must still parse cleanly, with the new fields defaulting to
// their zero values - never an unmarshal error, never a panic.
func TestManifestBackwardCompatMissingNewFields(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	// Hand-authored JSON with none of the three new keys present, standing in for a
	// manifest an old build of Provision/Teardown actually wrote to S3.
	old := `{"scenario":"fresh","config_name":"min_default","to_ref":"v6.1-release","delete_after":"2026-06-25T00:00:00Z","applied_ref":"v6.1-release","namespace":"dozuki","account_id":"076000000000","region":"us-east-1","dr_region":"us-west-2"}`
	s.mu.Lock()
	s.m["run1-min/"] = []byte(old)
	s.mu.Unlock()

	got, ok, err := s.Load(ctx, "run1-min/")
	if err != nil {
		t.Fatalf("load of a pre-fix manifest must not error: %v", err)
	}
	if !ok {
		t.Fatal("load of a pre-fix manifest must report ok=true")
	}
	if got.AppliedCustomer != "" {
		t.Fatalf("AppliedCustomer = %q, want zero value on a manifest that never wrote it", got.AppliedCustomer)
	}
	if got.KeepOnFailure || got.KeepOnFailureRecorded {
		t.Fatalf("KeepOnFailure=%v KeepOnFailureRecorded=%v, want both false on a manifest that never wrote them", got.KeepOnFailure, got.KeepOnFailureRecorded)
	}
	// Everything the old manifest DID carry must still come through untouched.
	if got.Scenario != "fresh" || got.ConfigName != "min_default" || got.ToRef != "v6.1-release" {
		t.Fatalf("pre-existing fields corrupted by the new ones: %+v", got)
	}
}

// TestTeardownRefAndSideProvisionOnlyFailure simulates the manifest state left behind
// when an upgrade-scenario Provision applied the baseline and saved AppliedRef/
// AppliedSide, then failed before Upgrade ever ran (Provision's validation step
// erroring, say). A re-entrant teardown reading this manifest fresh (its own process,
// no in-memory state from the failed run) must destroy against the BASELINE ref/side.
func TestTeardownRefAndSideProvisionOnlyFailure(t *testing.T) {
	rm := &RunManifest{
		Scenario: "upgrade", FromRef: "v6.0.3", ToRef: "v6.1-release",
		AppliedRef: "v6.0.3", AppliedSide: string(SideBaseline),
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide error = %v, want nil", err)
	}
	if ref != "v6.0.3" || side != SideBaseline {
		t.Fatalf("teardownRefAndSide = (%q, %q), want (v6.0.3, baseline)", ref, side)
	}
}

// TestTeardownRefAndSideCompletedUpgrade simulates the manifest state after Upgrade
// ran to completion (AppliedRef/AppliedSide both flipped to the target). Teardown must
// destroy against the TARGET ref/side.
func TestTeardownRefAndSideCompletedUpgrade(t *testing.T) {
	rm := &RunManifest{
		Scenario: "upgrade", FromRef: "v6.0.3", ToRef: "v6.1-release",
		AppliedRef: "v6.1-release", AppliedSide: string(SideTarget),
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide error = %v, want nil", err)
	}
	if ref != "v6.1-release" || side != SideTarget {
		t.Fatalf("teardownRefAndSide = (%q, %q), want (v6.1-release, target)", ref, side)
	}
}

// TestTeardownRefAndSideFromRefEqualsToRefExplicitSideWins is the case Part 2 exists
// for: a flavor flip applied with no ref bump at all (FromRef == ToRef), so the ref
// string alone cannot say which side is currently applied. A provision-only failure
// here still explicitly recorded AppliedSide=baseline; teardownRefAndSide must trust
// that recorded field rather than falling back (the fallback can't distinguish this
// case from "already on target" - see the next test).
func TestTeardownRefAndSideFromRefEqualsToRefExplicitSideWins(t *testing.T) {
	rm := &RunManifest{
		Scenario: "upgrade", FromRef: "v9.0", ToRef: "v9.0",
		AppliedRef: "v9.0", AppliedSide: string(SideBaseline),
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide error = %v, want nil", err)
	}
	if ref != "v9.0" || side != SideBaseline {
		t.Fatalf("teardownRefAndSide = (%q, %q), want (v9.0, baseline) - the explicit AppliedSide field must win when refs are equal", ref, side)
	}
}

// TestTeardownRefAndSideRejectsMalformedAppliedSide covers a manifest whose
// applied_side JSON field was hand-edited or corrupted to a value that is neither
// "baseline" nor "target". teardownRefAndSide must fail loud with a clear error
// rather than returning a Side value nothing downstream recognizes.
func TestTeardownRefAndSideRejectsMalformedAppliedSide(t *testing.T) {
	rm := &RunManifest{
		Scenario: "upgrade", FromRef: "v6.0.3", ToRef: "v6.1-release",
		AppliedRef: "v6.0.3", AppliedSide: "bogus",
	}
	_, _, err := teardownRefAndSide(rm)
	if err == nil {
		t.Fatal("teardownRefAndSide error = nil, want an error for malformed applied_side")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %q does not mention the malformed value", err.Error())
	}
}

// TestTeardownRefAndSideFallbackMatchesPreSideSemantics covers manifests written
// before AppliedSide existed (AppliedSide == ""). The fallback must reproduce exactly
// today's AppliedRef-only behavior, including the FromRef==ToRef edge case where the
// old logic could not tell baseline from target and defaulted to treating the run as
// already on target.
func TestTeardownRefAndSideFallbackMatchesPreSideSemantics(t *testing.T) {
	cases := []struct {
		name     string
		rm       *RunManifest
		wantRef  string
		wantSide Side
	}{
		{
			name:     "upgrade scenario, applied ref is FromRef, refs differ -> baseline",
			rm:       &RunManifest{Scenario: "upgrade", FromRef: "v6.0.3", ToRef: "v6.1-release", AppliedRef: "v6.0.3"},
			wantRef:  "v6.0.3",
			wantSide: SideBaseline,
		},
		{
			name:     "upgrade scenario, applied ref is ToRef -> target",
			rm:       &RunManifest{Scenario: "upgrade", FromRef: "v6.0.3", ToRef: "v6.1-release", AppliedRef: "v6.1-release"},
			wantRef:  "v6.1-release",
			wantSide: SideTarget,
		},
		{
			name:     "upgrade scenario, FromRef == ToRef -> target (refs alone are ambiguous; old behavior defaults target)",
			rm:       &RunManifest{Scenario: "upgrade", FromRef: "v9.0", ToRef: "v9.0", AppliedRef: "v9.0"},
			wantRef:  "v9.0",
			wantSide: SideTarget,
		},
		{
			name:     "fresh scenario -> target",
			rm:       &RunManifest{Scenario: "fresh", ToRef: "v7.1.0", AppliedRef: "v7.1.0"},
			wantRef:  "v7.1.0",
			wantSide: SideTarget,
		},
		{
			name:     "no successful apply recorded -> falls back to ToRef, target",
			rm:       &RunManifest{Scenario: "upgrade", FromRef: "v6.0.3", ToRef: "v6.1-release"},
			wantRef:  "v6.1-release",
			wantSide: SideTarget,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, side, err := teardownRefAndSide(c.rm)
			if err != nil {
				t.Fatalf("teardownRefAndSide error = %v, want nil", err)
			}
			if ref != c.wantRef || side != c.wantSide {
				t.Fatalf("teardownRefAndSide = (%q, %q), want (%q, %q)", ref, side, c.wantRef, c.wantSide)
			}
		})
	}
}

// TestManifestRoundTripAppliedSide proves AppliedSide survives a Save/Load cycle like
// every other manifest field, and that a pre-existing manifest JSON blob (no
// applied_side key at all) loads with it defaulting to empty rather than erroring.
func TestManifestRoundTripAppliedSide(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	want := &RunManifest{ConfigName: "min_default", AppliedRef: "v6.0.3", AppliedSide: string(SideBaseline)}
	if err := s.Save(ctx, "run1-min/", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := s.Load(ctx, "run1-min/")
	if err != nil || !ok {
		t.Fatalf("load after save: ok=%v err=%v", ok, err)
	}
	if got.AppliedSide != string(SideBaseline) {
		t.Fatalf("AppliedSide = %q, want %q", got.AppliedSide, SideBaseline)
	}

	old := `{"scenario":"upgrade","config_name":"min_default","applied_ref":"v6.0.3"}`
	s.mu.Lock()
	s.m["run2-min/"] = []byte(old)
	s.mu.Unlock()
	got2, ok2, err2 := s.Load(ctx, "run2-min/")
	if err2 != nil || !ok2 {
		t.Fatalf("load of pre-AppliedSide manifest: ok=%v err=%v", ok2, err2)
	}
	if got2.AppliedSide != "" {
		t.Fatalf("AppliedSide = %q, want empty on a manifest that never wrote it", got2.AppliedSide)
	}
}
