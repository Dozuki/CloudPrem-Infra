package harness

import (
	"context"
	"encoding/json"
	"sync"
)

const ManifestObjectName = "harness-manifest.json"

// RunManifest is the durable, cross-phase state for one (run, config). It lives in
// S3 under the run's state prefix so any phase pod can reconstruct what an earlier
// phase established. Fields here are exactly those NOT re-derivable from live TF
// outputs: the scenario, the resolved refs, the shared deleteAfter, which ref is
// currently applied (drives teardown), and the pre-upgrade baseline helm revision
// (gone once the upgrade applies, but needed by the upgrade proof).
type RunManifest struct {
	Scenario     string `json:"scenario"` // "upgrade" | "fresh"
	ConfigName   string `json:"config_name"`
	FromRef      string `json:"from_ref"` // empty for fresh
	ToRef        string `json:"to_ref"`
	DeleteAfter  string `json:"delete_after"` // RFC3339, shared by both worktrees
	AppliedRef   string `json:"applied_ref"`  // ref whose code matches deployed state
	BaselineRev  int    `json:"baseline_rev"` // helm revision before upgrade (0 until set)
	Namespace    string `json:"namespace"`
	AccountID    string `json:"account_id"`
	Region       string `json:"region"`
	DRRegion     string `json:"dr_region"`
	RestoreDrill bool   `json:"restore_drill"`
	EnableDR     bool   `json:"enable_dr"`
}

// ManifestStore persists a RunManifest keyed by state prefix (e.g. "run1-min/").
type ManifestStore interface {
	Load(ctx context.Context, statePrefix string) (*RunManifest, bool, error)
	Save(ctx context.Context, statePrefix string, m *RunManifest) error
}

// MemStore is an in-memory ManifestStore for tests and local dry-runs.
type MemStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemStore() *MemStore { return &MemStore{m: map[string][]byte{}} }

func (s *MemStore) Load(_ context.Context, statePrefix string) (*RunManifest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[statePrefix]
	if !ok {
		return nil, false, nil
	}
	var rm RunManifest
	if err := json.Unmarshal(b, &rm); err != nil {
		return nil, false, err
	}
	return &rm, true, nil
}

func (s *MemStore) Save(_ context.Context, statePrefix string, m *RunManifest) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[statePrefix] = b
	return nil
}
