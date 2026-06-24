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
