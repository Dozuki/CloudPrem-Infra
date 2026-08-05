package harness

import (
	"context"
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
		BaselineRev: 0, Namespace: "dozuki", AccountID: "076248559428",
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
	old := `{"scenario":"fresh","config_name":"min_default","to_ref":"v6.1-release","delete_after":"2026-06-25T00:00:00Z","applied_ref":"v6.1-release","namespace":"dozuki","account_id":"076248559428","region":"us-east-1","dr_region":"us-west-2"}`
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
