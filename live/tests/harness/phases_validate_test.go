package harness

import (
	"context"
	"strings"
	"testing"
)

func TestValidateRequiresManifest(t *testing.T) {
	ctx := context.Background()
	m := &Matrix{Configs: []Config{{Name: "min_default", Env: "min"}}}
	p := PhaseParams{Matrix: m, Store: NewMemStore(), ConfigName: "min_default", RunID: "run1"}
	if err := p.Validate(ctx); err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Fatalf("want 'no manifest', got %v", err)
	}
}
