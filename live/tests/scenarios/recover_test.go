package scenarios

import (
	"os"
	"testing"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
)

// TestRecover drills the decided recovery path end to end: fresh source stack with DR
// -> promotion drill (data-verified) -> snapshot the promoted cluster -> stand up a NEW
// stack in the DR region from that snapshot + the adopted *-dr-* buckets -> prove the
// rebuilt app still serves every heartbeat the drill verified. This is the P3 rebuild
// executed for real, not scaffold-reviewed. ~4h wall clock, two full stacks - run it
// on demand, not in every sweep.
// Integration test: requires RUN_INTEGRATION=1, DDVtest creds, and Plan 1 applied.
func TestRecover(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION") != "1" {
		t.Skip("set RUN_INTEGRATION=1 to run (needs DDVtest creds + applied foundation)")
	}
	repoDir := env("REPO_DIR", mustAbsRepoRoot(t))
	accountID := mustEnv(t, "DDVTEST_ACCOUNT_ID")
	profile := env("AWS_PROFILE", "ddvtest")
	runID := mustEnv(t, "RUN_ID")
	namespace := env("APP_NAMESPACE", "dozuki")

	m, err := harness.LoadMatrix(repoDir + "/live/tests/matrix.yaml")
	if err != nil {
		t.Fatalf("load matrix: %v", err)
	}
	tags, err := harness.NewestTags(repoDir)
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	toRef, err := harness.ResolveRef(env("TO_REF", m.Defaults.ToRef), tags)
	if err != nil {
		t.Fatalf("to_ref: %v", err)
	}

	source := env("SOURCE_CONFIG", "recover_source")
	rebuild := env("RECOVER_CONFIG", "recover")
	cfg, err := m.Config(source)
	if err != nil {
		t.Fatalf("config %s: %v", source, err)
	}
	err = harness.RunRecovery(harness.RunParams{
		RepoDir:      repoDir,
		Matrix:       m,
		ConfigName:   source,
		ToRef:        toRef,
		AccountID:    accountID,
		Profile:      profile,
		RunID:        runID + "-recover-" + source,
		Namespace:    namespace,
		DRRegion:     m.Defaults.DRRegion,
		RestoreDrill: cfg.HarnessFlag("restore_drill"),
		EnableDR:     cfg.HarnessFlag("enable_dr"),
	}, rebuild)
	if err != nil {
		t.Fatalf("recover %s->%s (%s): %v", source, rebuild, toRef, err)
	}
}
