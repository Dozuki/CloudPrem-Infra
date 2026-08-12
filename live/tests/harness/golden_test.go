package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// goldenConfigs are the pre-existing configs this test pins - the ones that predate
// Side/BaselineVersions/TargetVersions entirely (every config in ../matrix.yaml except
// slim_fresh and slim_upgrade). TestRegressionConfigsWithoutSideMapsAreUnaffected
// (config_test.go) already proves baseline-render == target-render for these; it does
// NOT prove the render is unchanged versus before the Side feature existed at all, which
// is the actual guarantee these configs need - a caller that never mentions Side must
// keep getting exactly the terraform inputs it always got. That is what this test pins,
// byte-for-byte, against a golden file per config.
var goldenConfigs = []string{"min_default", "bi_ha", "recover_source", "recover", "full"}

func goldenDir() string {
	return filepath.Join("testdata", "golden")
}

func goldenPath(configName string) string {
	return filepath.Join(goldenDir(), configName+".golden")
}

// renderGolden renders RenderEnvHCL(m.MergedInputs(cfg, ref, side)) for every ref in
// m.Versions (sorted, for a deterministic file) and both sides, as one file per config.
func renderGolden(m *Matrix, cfg Config) string {
	refs := make([]string, 0, len(m.Versions))
	for ref := range m.Versions {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	var b strings.Builder
	for _, ref := range refs {
		for _, side := range []Side{SideBaseline, SideTarget} {
			fmt.Fprintf(&b, "=== ref=%s side=%s ===\n", ref, side)
			b.WriteString(RenderEnvHCL(m.MergedInputs(cfg, ref, side)))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestGoldenRenderUnchangedForPreExistingConfigs is the durable regression guard: it
// re-renders every pre-existing config (goldenConfigs above - never slim_fresh/
// slim_upgrade, which legitimately carry the new baseline_versions/target_versions
// keys) against every ref in the real ../matrix.yaml's `versions:` map, on both sides,
// and diffs the result against a golden file checked in under testdata/golden/.
//
// The golden files were captured from the CURRENT code, which at the time of writing
// had already been verified byte-identical to origin/master's render for these five
// configs (see TestRegressionConfigsWithoutSideMapsAreUnaffected) - so capturing it now
// is capturing the correct pre-change bytes, not merely "whatever the code does today".
//
// Regenerate the goldens (e.g. after a deliberate, reviewed change to version_defaults
// or a config's feature_flags in matrix.yaml) by running:
//
//	UPDATE_GOLDEN=1 go test ./harness/ -run TestGoldenRenderUnchangedForPreExistingConfigs
func TestGoldenRenderUnchangedForPreExistingConfigs(t *testing.T) {
	m, err := LoadMatrix("../matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix(../matrix.yaml): %v", err)
	}
	if len(m.Versions) == 0 {
		t.Fatal("../matrix.yaml has no versions entries - golden guard would check nothing")
	}

	update := os.Getenv("UPDATE_GOLDEN") == "1"
	if update {
		if err := os.MkdirAll(goldenDir(), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", goldenDir(), err)
		}
	}

	checked := 0
	for _, name := range goldenConfigs {
		cfg, err := m.Config(name)
		if err != nil {
			t.Fatalf("Config(%s): %v (goldenConfigs must name only configs present in ../matrix.yaml)", name, err)
		}
		if len(cfg.BaselineVersions) > 0 || len(cfg.TargetVersions) > 0 {
			t.Fatalf("config %s carries baseline_versions/target_versions - it does not belong in goldenConfigs (slim-only configs are deliberately excluded)", name)
		}
		checked++

		got := renderGolden(m, cfg)
		path := goldenPath(name)

		if update {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatalf("write golden %s: %v", path, err)
			}
			continue
		}

		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to generate it)", path, err)
		}
		if got != string(want) {
			t.Errorf("config %s: render drifted from golden %s.\nRun with UPDATE_GOLDEN=1 to inspect/regenerate after confirming the drift is intentional.\n--- want ---\n%s\n--- got ---\n%s",
				name, path, want, got)
		}
	}
	if checked == 0 {
		t.Fatal("goldenConfigs is empty - golden guard did not actually check anything")
	}
}
