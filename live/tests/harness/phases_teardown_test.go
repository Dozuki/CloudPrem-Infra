package harness

import (
	"context"
	"testing"
)

func TestTeardownNoManifestIsNoop(t *testing.T) {
	ctx := context.Background()
	m := &Matrix{Configs: []Config{{Name: "min_default", Env: "min"}}}
	p := PhaseParams{Matrix: m, Store: NewMemStore(), ConfigName: "min_default", RunID: "run1"}
	if err := p.Teardown(ctx, false, false); err != nil {
		t.Fatalf("teardown with no manifest should be a no-op, got %v", err)
	}
}
