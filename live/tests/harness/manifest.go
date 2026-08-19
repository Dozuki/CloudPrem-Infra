package harness

import (
	"context"
	"encoding/json"
	"fmt"
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
	Scenario    string `json:"scenario"` // "upgrade" | "fresh" | "recover"
	ConfigName  string `json:"config_name"`
	FromRef     string `json:"from_ref"` // empty for fresh
	ToRef       string `json:"to_ref"`
	DeleteAfter string `json:"delete_after"` // RFC3339, shared by both worktrees
	AppliedRef  string `json:"applied_ref"`  // ref whose code matches deployed state
	// AppliedSide records which side (Side) AppliedRef represents. Refs alone cannot
	// encode this when FromRef == ToRef (a docs-only bump, or a flavor flip applied in
	// place with no version bump), so this is durable manifest state written at both
	// AppliedRef write points (Provision, Upgrade - phases.go) rather than derived from
	// the ref string. Empty on a manifest written before this field existed; callers
	// must use teardownRefAndSide (or reimplement its fallback) rather than reading
	// this field raw. Must be "" (predates the field), "baseline", or "target" -
	// teardownRefAndSide rejects any other value rather than silently converting it.
	AppliedSide string `json:"applied_side,omitempty"`

	// ApplyingRef/ApplyingSide record the ref an apply is ABOUT to run with, written
	// before the apply starts and left in place after it. AppliedRef is only written
	// once an apply RETURNS, which leaves a killed apply with no record at all - and a
	// killed apply is precisely when there is orphaned infrastructure to destroy.
	// harness-bi-ha-hftj2 died that way (superseded mid-apply, SIGTERM at
	// 2026-08-17T01:34:29Z): applied_ref stayed "", so both teardownRefAndSide and
	// TeardownRepoRef fell through to ToRef and the teardown ran the TARGET's code
	// against a stack the BASELINE had built (bd Lodestar-1xm.36).
	//
	// Deliberately a SEPARATE field rather than writing AppliedRef early:
	// validatePreconditions treats "AppliedRef == ToRef" as proof the target apply
	// SUCCEEDED, and an early write would turn that guard into "an upgrade was
	// attempted", letting Validate render the target side against a half-applied stack.
	// The two facts are different and the manifest now keeps both.
	ApplyingRef  string `json:"applying_ref,omitempty"`
	ApplyingSide string `json:"applying_side,omitempty"`
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

	// Residuals is the last boundary check's report (residuals.go). Kept on the
	// manifest rather than only in the pod log because the manifest outlives the
	// Workflow CR, and a leaked stack is usually looked at days later.
	Residuals *ResidualReport `json:"residuals,omitempty"`

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

// teardownRefAndSide resolves the ref whose code matches deployed state (AppliedRef,
// falling back to ToRef for a manifest that never recorded a successful apply) and the
// Side that ref represents, for Teardown's prepareWorktreeSide call. AppliedSide wins
// when present. A manifest written before AppliedSide existed falls back to exactly
// today's (pre-Side) AppliedRef semantics: baseline iff (scenario == "upgrade" AND
// AppliedRef == FromRef AND FromRef != ToRef) - the FromRef != ToRef guard matters
// because when the refs are equal, AppliedRef == FromRef is uninformative (it also
// equals ToRef), and today's behavior in that ambiguous case is to treat the run as
// already on its target side.
//
// Why the fallback can never pick a "wrong" side: AppliedSide and the per-side
// override maps (Config.BaselineVersions/TargetVersions) shipped in the same change.
// So any manifest lacking AppliedSide necessarily belongs to a config with nil side
// maps - one of the pre-existing configs that predates Side entirely - and for those,
// MergedInputs's side-map merge layer is a no-op on both sides (sideVersions returns
// nil either way), so baseline and target render identically regardless of which one
// this fallback guesses. The fallback's guess can be wrong in the abstract; it can
// never be wrong in a way that changes the rendered env.hcl.
//
// The AppliedSide -> Side conversion below is a plain string-to-defined-type
// conversion, which Go never rejects at compile or run time even for a value that
// matches neither SideBaseline nor SideTarget - a hand-edited or corrupted manifest
// JSON blob could carry applied_side: "bogus" and this function would otherwise
// return that as a well-typed Side silently. Validated explicitly here so a malformed
// manifest fails teardown with a clear error instead of prepareWorktreeSide (or
// something further downstream) misbehaving on an unrecognized Side value.
func teardownRefAndSide(rm *RunManifest) (string, Side, error) {
	// ApplyingRef sits between AppliedRef and the ToRef fallback: a completed apply is
	// the best answer, an apply that STARTED and was killed is the next best (it names
	// the code that created whatever is now live), and ToRef is the last resort for a
	// manifest that predates both fields.
	ref, sideStr := rm.AppliedRef, rm.AppliedSide
	// An apply that STARTED and did not finish is the most recent thing to have touched
	// this infrastructure, so it wins over the last apply that completed. A completed
	// apply leaves ApplyingRef == AppliedRef (both writes name the same ref), so this
	// only fires when an apply is genuinely unfinished - a target upgrade killed after a
	// successful baseline provision would otherwise resolve to the BASELINE and repeat
	// the same wrong-code destroy in the opposite direction.
	if rm.ApplyingRef != "" && rm.ApplyingRef != rm.AppliedRef {
		ref, sideStr = rm.ApplyingRef, rm.ApplyingSide
	}
	if ref == "" {
		ref, sideStr = rm.ToRef, rm.AppliedSide
	}
	if sideStr != "" {
		side := Side(sideStr)
		if side != SideBaseline && side != SideTarget {
			return "", "", fmt.Errorf("manifest has malformed applied side %q (want %q or %q)", sideStr, SideBaseline, SideTarget)
		}
		return ref, side, nil
	}
	if rm.Scenario == "upgrade" && rm.AppliedRef == rm.FromRef && rm.FromRef != rm.ToRef {
		return ref, SideBaseline, nil
	}
	return ref, SideTarget, nil
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
