package harness

import (
	"context"
	"strings"
	"testing"
)

func TestUpgradeRequiresProvisionedUpgradeManifest(t *testing.T) {
	ctx := context.Background()
	m := &Matrix{Defaults: Defaults{Region: "us-east-1", EnvPath: "standard/us-east-1"},
		Configs: []Config{{Name: "min_default", Env: "min"}}}
	store := NewMemStore()
	p := PhaseParams{RepoDir: t.TempDir(), Matrix: m, Store: store, ConfigName: "min_default", RunID: "run1", Region: "us-east-1"}

	// No manifest yet → clear error.
	if err := p.Upgrade(ctx); err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Fatalf("want 'no manifest' error, got %v", err)
	}
	// Fresh scenario → refuse.
	cfg, _ := m.Config("min_default")
	_ = store.Save(ctx, p.statePrefix(cfg), &RunManifest{Scenario: "fresh", ToRef: "v7.1.0"})
	if err := p.Upgrade(ctx); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatalf("want 'fresh' refusal, got %v", err)
	}
}
