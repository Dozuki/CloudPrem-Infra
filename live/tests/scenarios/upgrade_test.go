package scenarios

import (
	"os"
	"testing"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
)

// TestUpgrade runs the dual-ref upgrade for the configs in CONFIGS (comma-sep,
// default min_default). Integration test: requires RUN_INTEGRATION=1, DDVtest
// creds, and Plan 1 applied. Skipped otherwise so `go test ./...` stays green.
func TestUpgrade(t *testing.T) {
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
	fromRef, err := harness.ResolveRef(env("FROM_REF", m.Defaults.FromRef), tags)
	if err != nil {
		t.Fatalf("from_ref: %v", err)
	}
	toRef, err := harness.ResolveRef(env("TO_REF", m.Defaults.ToRef), tags)
	if err != nil {
		t.Fatalf("to_ref: %v", err)
	}

	for _, name := range splitConfigs(env("CONFIGS", "min_default")) {
		name := name
		t.Run(name, func(t *testing.T) {
			err := harness.RunUpgrade(harness.RunParams{
				RepoDir:    repoDir,
				Matrix:     m,
				ConfigName: name,
				FromRef:    fromRef,
				ToRef:      toRef,
				AccountID:  accountID,
				Profile:    profile,
				RunID:      runID + "-" + name,
				Namespace:  namespace,
			})
			if err != nil {
				t.Fatalf("upgrade %s (%s->%s): %v", name, fromRef, toRef, err)
			}
		})
	}
}
