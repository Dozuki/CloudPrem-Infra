package scenarios

import (
	"os"
	"testing"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
)

// TestFresh runs a fresh single-instance deploy + validate + teardown for the
// configs in CONFIGS (comma-sep, default min_default) — no baseline/upgrade. It
// catches create-from-scratch regressions that an in-place upgrade can mask.
// Integration test: requires RUN_INTEGRATION=1, DDVtest creds, and Plan 1 applied.
// Skipped otherwise so `go test ./...` stays green.
func TestFresh(t *testing.T) {
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
	// Fresh deploys a single ref — the target. Reuse TO_REF (else matrix default).
	toRef, err := harness.ResolveRef(env("TO_REF", m.Defaults.ToRef), tags)
	if err != nil {
		t.Fatalf("to_ref: %v", err)
	}

	for _, name := range splitConfigs(env("CONFIGS", "min_default")) {
		name := name
		t.Run(name, func(t *testing.T) {
			cfg, err := m.Config(name)
			if err != nil {
				t.Fatalf("config %s: %v", name, err)
			}
			err = harness.RunFresh(harness.RunParams{
				RepoDir:      repoDir,
				Matrix:       m,
				ConfigName:   name,
				ToRef:        toRef,
				AccountID:    accountID,
				Profile:      profile,
				RunID:        runID + "-fresh-" + name,
				Namespace:    namespace,
				DRRegion:     m.Defaults.DRRegion,
				RestoreDrill: cfg.HarnessFlag("restore_drill"),
				EnableDR:     cfg.HarnessFlag("enable_dr"),
			})
			if err != nil {
				t.Fatalf("fresh %s (%s): %v", name, toRef, err)
			}
		})
	}
}
