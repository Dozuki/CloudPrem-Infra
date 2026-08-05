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
	Scenario     string `json:"scenario"` // "upgrade" | "fresh" | "recover"
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

	// Runtime terraform inputs merged into env.hcl on EVERY worktree render for this
	// (run, config) - the recovery stack's snapshot ARN and adopted buckets live here.
	// Persisted because teardown must re-render the exact env.hcl the apply used.
	ExtraInputs map[string]interface{} `json:"extra_inputs,omitempty"`

	// Aurora promotion-drill results, recorded so a later phase (the recovery rebuild)
	// can snapshot the promoted cluster and judge data survival against what the drill
	// verified - both are gone from live outputs once the drill has run.
	PromotedClusterID string `json:"promoted_cluster_id,omitempty"`
	DrillHeartbeats   int    `json:"drill_heartbeats,omitempty"`

	// AppliedCustomer is the salted "customer" feature-flag value actually used at apply
	// time (phases.go Provision), not a value recomputed from Config.Salted against
	// whatever the matrix checkout looks like today. The matrix is a live file: if a
	// config's customer flag is edited after a run starts and that run later leaks, a
	// fresh recompute would query AWS for a tag the run never carried, find nothing, and
	// silently misreport a real leak as torn down (the janitor's defect P2). Empty on a
	// manifest written before this field existed - callers must not guess a value in
	// that case, see janitor.go classify().
	AppliedCustomer string `json:"applied_customer,omitempty"`

	// KeepOnFailure/KeepOnFailureRecorded durably record the --keep-on-failure decision
	// a teardown call was given (phases.go Teardown, written on every call). Argo TTLs
	// the owning Workflow CR three days after it finishes (10-scenario.yaml
	// ttlStrategy), so a workflow-index lookup for this same fact goes blank well within
	// a weekend - long enough for a sweeper to destroy a stack a human deliberately left
	// up to debug (the janitor's defect P1). KeepOnFailureRecorded distinguishes
	// "recorded, and it was false" from "this manifest predates the field entirely" -
	// only the latter should fall back to any other source (the workflow index).
	KeepOnFailure         bool `json:"keep_on_failure,omitempty"`
	KeepOnFailureRecorded bool `json:"keep_on_failure_recorded,omitempty"`
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
