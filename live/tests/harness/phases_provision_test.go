package harness

import (
	"context"
	"testing"
)

func TestLoadOrInitManifestCreatesThenReuses(t *testing.T) {
	ctx := context.Background()
	m := &Matrix{Defaults: Defaults{ReaperTTLHours: 24}}
	store := NewMemStore()
	p := PhaseParams{Matrix: m, Store: store, ConfigName: "min_default", RunID: "run1"}
	cfg := Config{Name: "min_default", Env: "min"}

	rm, err := p.loadOrInitManifest(ctx, cfg, "upgrade", "v6.0.3", "v6.1-release", "2026-06-25T00:00:00Z")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if rm.Scenario != "upgrade" || rm.FromRef != "v6.0.3" || rm.DeleteAfter != "2026-06-25T00:00:00Z" {
		t.Fatalf("created manifest wrong: %+v", rm)
	}
	// Mutate + persist, then re-init must REUSE the stored deleteAfter, not the new arg.
	rm.BaselineRev = 7
	if err := store.Save(ctx, p.statePrefix(cfg), rm); err != nil {
		t.Fatal(err)
	}
	rm2, err := p.loadOrInitManifest(ctx, cfg, "upgrade", "v6.0.3", "v6.1-release", "9999-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if rm2.BaselineRev != 7 || rm2.DeleteAfter != "2026-06-25T00:00:00Z" {
		t.Fatalf("re-init should reuse stored manifest: %+v", rm2)
	}
}
