package harness

import (
	"reflect"
	"testing"
)

func TestLoadMatrixAndMergeInputs(t *testing.T) {
	m, err := LoadMatrix("testdata/matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	cfg, err := m.Config("min_default")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	base := m.MergedInputs(cfg, "v6.0", SideBaseline)
	if base["image_tag"] != "base-app" {
		t.Errorf("image_tag = %v, want base-app", base["image_tag"])
	}
	if base["enable_bi"] != false {
		t.Errorf("enable_bi = %v, want false", base["enable_bi"])
	}
	if _, ok := base["chart_version"]; ok {
		t.Errorf("chart_version should be absent on v6.0")
	}
	tgt := m.MergedInputs(cfg, "v6.1-release", SideTarget)
	if tgt["chart_version"] != "0.3.0" {
		t.Errorf("chart_version = %v, want 0.3.0", tgt["chart_version"])
	}
	if tgt["image_tag"] != "tgt-app" {
		t.Errorf("image_tag = %v, want tgt-app", tgt["image_tag"])
	}
}

func TestMergedInputsExcludesHarnessOnlyKeys(t *testing.T) {
	m, err := LoadMatrix("testdata/matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	full, err := m.Config("full")
	if err != nil {
		t.Fatalf("Config(full): %v", err)
	}
	inputs := m.MergedInputs(full, "v6.0", SideBaseline)

	// restore_drill is harness-only and must NOT appear in terraform inputs.
	if _, ok := inputs["restore_drill"]; ok {
		t.Errorf("restore_drill must be excluded from MergedInputs but was present")
	}
	// enable_dr is a real terraform var and must be included.
	if v, ok := inputs["enable_dr"]; !ok || v != true {
		t.Errorf("enable_dr = %v (present=%v), want true", v, ok)
	}
	// HarnessFlag must read restore_drill from the config.
	if !full.HarnessFlag("restore_drill") {
		t.Errorf("HarnessFlag(restore_drill) = false, want true")
	}
	if full.HarnessFlag("nonexistent") {
		t.Errorf("HarnessFlag(nonexistent) = true, want false")
	}
}

func TestWithDeleteAfter(t *testing.T) {
	out := withDeleteAfter(map[string]interface{}{"x": 1}, "2026-06-24T00:00:00Z")
	if out["delete_after"] != "2026-06-24T00:00:00Z" {
		t.Errorf("delete_after = %v", out["delete_after"])
	}
}

func TestVersionDefaults(t *testing.T) {
	m := &Matrix{
		VersionDefaults: map[string]interface{}{
			"image_tag":     "default-app",
			"chart_version": "0.4.1",
		},
		Versions: map[string]map[string]interface{}{
			"v6.0": {"image_tag": "old-app"}, // overrides image_tag; inherits chart_version
		},
		Configs: []Config{{Name: "min_default", Env: "min", FeatureFlags: map[string]interface{}{"enable_bi": false}}},
	}
	cfg, _ := m.Config("min_default")

	// A ref with NO explicit entry inherits all defaults.
	newRef := m.MergedInputs(cfg, "v7.1.2", SideTarget)
	if newRef["image_tag"] != "default-app" {
		t.Errorf("v7.1.2 image_tag = %v, want default-app (inherited)", newRef["image_tag"])
	}
	if newRef["chart_version"] != "0.4.1" {
		t.Errorf("v7.1.2 chart_version = %v, want 0.4.1 (inherited)", newRef["chart_version"])
	}

	// A ref-specific key overrides the default; unspecified keys still inherit.
	old := m.MergedInputs(cfg, "v6.0", SideBaseline)
	if old["image_tag"] != "old-app" {
		t.Errorf("v6.0 image_tag = %v, want old-app (override)", old["image_tag"])
	}
	if old["chart_version"] != "0.4.1" {
		t.Errorf("v6.0 chart_version = %v, want 0.4.1 (inherited default)", old["chart_version"])
	}

	// EffectiveVersionVar with no config/side overrides: ref override wins, else default.
	if got := m.EffectiveVersionVar(cfg, "v6.0", SideBaseline, "image_tag"); got != "old-app" {
		t.Errorf("EffectiveVersionVar(v6.0,image_tag) = %v, want old-app", got)
	}
	if got := m.EffectiveVersionVar(cfg, "v7.1.2", SideTarget, "chart_version"); got != "0.4.1" {
		t.Errorf("EffectiveVersionVar(v7.1.2,chart_version) = %v, want 0.4.1", got)
	}

	// VersionProfileExists is true for ANY ref once defaults are set.
	if !m.VersionProfileExists("v7.1.2") {
		t.Errorf("VersionProfileExists(v7.1.2) = false, want true (defaults set)")
	}

	// Without defaults, only refs with an explicit entry resolve.
	empty := &Matrix{Versions: map[string]map[string]interface{}{"v6.0": {}}}
	if empty.VersionProfileExists("v7.1.2") {
		t.Errorf("VersionProfileExists(v7.1.2) = true with no defaults, want false")
	}
	if !empty.VersionProfileExists("v6.0") {
		t.Errorf("VersionProfileExists(v6.0) = false, want true (explicit entry)")
	}
}

// TestSideVersionsWinOverEveryOtherLayer sets the SAME key in all four merge layers
// (feature_flags, version_defaults, versions[ref], and the config's side map) and
// proves the side map wins for both MergedInputs and EffectiveVersionVar, in both
// directions (baseline map vs target map resolve independently).
func TestSideVersionsWinOverEveryOtherLayer(t *testing.T) {
	m := &Matrix{
		VersionDefaults: map[string]interface{}{"app_image_flavor": "from-defaults"},
		Versions: map[string]map[string]interface{}{
			"v9.0": {"app_image_flavor": "from-versions-ref"},
		},
		Configs: []Config{{
			Name:             "flip",
			Env:              "min",
			FeatureFlags:     map[string]interface{}{"app_image_flavor": "from-feature-flags"},
			BaselineVersions: map[string]interface{}{"app_image_flavor": "from-baseline-map"},
			TargetVersions:   map[string]interface{}{"app_image_flavor": "from-target-map"},
		}},
	}
	cfg, err := m.Config("flip")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}

	if got := m.MergedInputs(cfg, "v9.0", SideBaseline)["app_image_flavor"]; got != "from-baseline-map" {
		t.Errorf("MergedInputs baseline app_image_flavor = %v, want from-baseline-map (side map must win over feature_flags, version_defaults, and versions[ref])", got)
	}
	if got := m.MergedInputs(cfg, "v9.0", SideTarget)["app_image_flavor"]; got != "from-target-map" {
		t.Errorf("MergedInputs target app_image_flavor = %v, want from-target-map", got)
	}
	if got := m.EffectiveVersionVar(cfg, "v9.0", SideBaseline, "app_image_flavor"); got != "from-baseline-map" {
		t.Errorf("EffectiveVersionVar baseline = %v, want from-baseline-map", got)
	}
	if got := m.EffectiveVersionVar(cfg, "v9.0", SideTarget, "app_image_flavor"); got != "from-target-map" {
		t.Errorf("EffectiveVersionVar target = %v, want from-target-map", got)
	}
}

// TestEffectiveVersionVarFallsThroughLayers proves the same precedence chain applies
// key-by-key even when the config's side maps don't set the key at all - each lower
// layer must still be reachable.
func TestEffectiveVersionVarFallsThroughLayers(t *testing.T) {
	m := &Matrix{
		VersionDefaults: map[string]interface{}{"chart_version": "0.4.1", "image_tag": "default-app"},
		Versions: map[string]map[string]interface{}{
			"v6.0": {"image_tag": "ref-app"},
		},
		Configs: []Config{{
			Name:             "partial",
			Env:              "min",
			FeatureFlags:     map[string]interface{}{"enable_bi": false},
			TargetVersions:   map[string]interface{}{"app_image_flavor": "slim"},
			BaselineVersions: nil,
		}},
	}
	cfg, _ := m.Config("partial")

	if got := m.EffectiveVersionVar(cfg, "v6.0", SideBaseline, "image_tag"); got != "ref-app" {
		t.Errorf("image_tag = %v, want ref-app (versions[ref] layer)", got)
	}
	if got := m.EffectiveVersionVar(cfg, "v6.0", SideBaseline, "chart_version"); got != "0.4.1" {
		t.Errorf("chart_version = %v, want 0.4.1 (version_defaults layer)", got)
	}
	if got := m.EffectiveVersionVar(cfg, "v6.0", SideTarget, "app_image_flavor"); got != "slim" {
		t.Errorf("app_image_flavor = %v, want slim (target side map)", got)
	}
	if got := m.EffectiveVersionVar(cfg, "v6.0", SideBaseline, "app_image_flavor"); got != nil {
		t.Errorf("app_image_flavor (baseline) = %v, want nil (baseline map unset, no other layer sets it)", got)
	}
}

// TestRegressionConfigsWithoutSideMapsAreUnaffected is the mandatory byte-identical
// guard: every config in the REAL matrix.yaml that has no baseline_versions/
// target_versions (every pre-existing config: min_default, bi_ha, recover_source,
// recover, full) must produce the exact same MergedInputs map on both sides as a
// config-unaware caller would have gotten before Side existed. Since sideVersions
// returns nil for these configs, both sides fold to a no-op merge layer; this proves
// that in practice rather than by inspection, against the production matrix these
// changes ship with.
func TestRegressionConfigsWithoutSideMapsAreUnaffected(t *testing.T) {
	m, err := LoadMatrix("../matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix(../matrix.yaml): %v", err)
	}
	checked := 0
	for _, cfg := range m.Configs {
		if len(cfg.BaselineVersions) > 0 || len(cfg.TargetVersions) > 0 {
			continue // slim_fresh / slim_upgrade - these DO carry the new keys
		}
		checked++
		for _, ref := range []string{m.Defaults.FromRef, m.Defaults.ToRef, "v6.0", "v6.1-release", "v8.3.0"} {
			base := m.MergedInputs(cfg, ref, SideBaseline)
			tgt := m.MergedInputs(cfg, ref, SideTarget)
			if len(base) != len(tgt) {
				t.Fatalf("config %s ref %s: baseline/target maps differ in size (%d vs %d) though this config sets no side overrides", cfg.Name, ref, len(base), len(tgt))
			}
			for k, v := range base {
				if !reflect.DeepEqual(tgt[k], v) {
					t.Errorf("config %s ref %s key %q: baseline=%v target=%v, want identical (no side overrides set)", cfg.Name, ref, k, v, tgt[k])
				}
			}
			// Also pin against RenderEnvHCL: the actual bytes written to env.hcl must be
			// identical between sides for these configs.
			if hclBase, hclTgt := RenderEnvHCL(base), RenderEnvHCL(tgt); hclBase != hclTgt {
				t.Errorf("config %s ref %s: rendered env.hcl differs between sides though no side overrides are set", cfg.Name, ref)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no configs without baseline_versions/target_versions found in matrix.yaml - regression guard did not actually check anything")
	}
}

// isLowerHex reports whether s is entirely lowercase hex digits.
func isLowerHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

func TestSaltedIsDeterministic(t *testing.T) {
	cfg := Config{Name: "min_default", FeatureFlags: map[string]interface{}{"customer": "smoke"}}
	a := cfg.Salted("run-123")
	b := cfg.Salted("run-123")
	ca, _ := a.FeatureFlags["customer"].(string)
	cb, _ := b.FeatureFlags["customer"].(string)
	if ca != cb {
		t.Errorf("Salted(%q) not deterministic: got %q then %q", "run-123", ca, cb)
	}
	if ca == "smoke" {
		t.Errorf("Salted did not append anything to customer")
	}
}

func TestSaltedDiffersByRunID(t *testing.T) {
	cfg := Config{Name: "min_default", FeatureFlags: map[string]interface{}{"customer": "smoke"}}
	a, _ := cfg.Salted("run-123").FeatureFlags["customer"].(string)
	b, _ := cfg.Salted("run-456").FeatureFlags["customer"].(string)
	if a == b {
		t.Errorf("different runIDs produced the same salted customer %q", a)
	}
}

func TestSaltedDiffersByConfigName(t *testing.T) {
	flags := map[string]interface{}{"customer": "smoke"}
	cfg1 := Config{Name: "min_default", FeatureFlags: flags}
	cfg2 := Config{Name: "full", FeatureFlags: flags}
	a, _ := cfg1.Salted("run-123").FeatureFlags["customer"].(string)
	b, _ := cfg2.Salted("run-123").FeatureFlags["customer"].(string)
	if a == b {
		t.Errorf("different config names produced the same salted customer %q (salt must include c.Name)", a)
	}
}

func TestSaltedFiveCharBaseAppendsFourHexChars(t *testing.T) {
	cfg := Config{Name: "min_default", FeatureFlags: map[string]interface{}{"customer": "smoke"}}
	got, _ := cfg.Salted("run-123").FeatureFlags["customer"].(string)
	if len(got) != 9 {
		t.Fatalf("len(%q) = %d, want 9 (5-char base + 4-char salt)", got, len(got))
	}
	suffix := got[5:]
	if !isLowerHex(suffix) {
		t.Errorf("suffix %q is not lowercase hex", suffix)
	}
}

func TestSaltedEightCharBaseAppendsTwoHexChars(t *testing.T) {
	for _, base := range []string{"smokesrc", "smokerec"} {
		cfg := Config{Name: "recover_source", FeatureFlags: map[string]interface{}{"customer": base}}
		got, _ := cfg.Salted("run-123").FeatureFlags["customer"].(string)
		if len(got) != 10 {
			t.Fatalf("base %q: len(%q) = %d, want 10 (8-char base + 2-char salt)", base, got, len(got))
		}
		suffix := got[8:]
		if !isLowerHex(suffix) {
			t.Errorf("base %q: suffix %q is not lowercase hex", base, suffix)
		}
	}
}

func TestSaltedNoOpAtOrOverTenCharBase(t *testing.T) {
	for _, base := range []string{"tenletters", "elevenchars1"} {
		cfg := Config{Name: "min_default", FeatureFlags: map[string]interface{}{"customer": base}}
		got, _ := cfg.Salted("run-123").FeatureFlags["customer"].(string)
		if got != base {
			t.Errorf("base %q: Salted changed it to %q, want unchanged (already at/over the 10-char cap)", base, got)
		}
	}
}

func TestSaltedNoCustomerFlag(t *testing.T) {
	cfg := Config{Name: "min_default", FeatureFlags: map[string]interface{}{"enable_bi": true}}
	got := cfg.Salted("run-123")
	if _, ok := got.FeatureFlags["customer"]; ok {
		t.Errorf("customer flag appeared out of nowhere: %v", got.FeatureFlags["customer"])
	}
	if got.FeatureFlags["enable_bi"] != true {
		t.Errorf("unrelated flag enable_bi was disturbed: %v", got.FeatureFlags["enable_bi"])
	}
}

func TestSaltedNonStringCustomerFlag(t *testing.T) {
	cfg := Config{Name: "min_default", FeatureFlags: map[string]interface{}{"customer": true}}
	got := cfg.Salted("run-123")
	if got.FeatureFlags["customer"] != true {
		t.Errorf("non-string customer flag was mutated: %v", got.FeatureFlags["customer"])
	}
}

func TestSaltedEmptyRunIDIsNoOp(t *testing.T) {
	cfg := Config{Name: "min_default", FeatureFlags: map[string]interface{}{"customer": "smoke"}}
	got := cfg.Salted("")
	if got.FeatureFlags["customer"] != "smoke" {
		t.Errorf("empty runID should be a no-op, got %v", got.FeatureFlags["customer"])
	}
}

func TestSaltedCopiesFeatureFlagsMap(t *testing.T) {
	orig := map[string]interface{}{"customer": "smoke"}
	cfg := Config{Name: "min_default", FeatureFlags: orig}
	got := cfg.Salted("run-123")
	got.FeatureFlags["injected"] = "oops"
	if _, ok := orig["injected"]; ok {
		t.Errorf("Salted's FeatureFlags map shares storage with the original config's map")
	}
	if _, ok := cfg.FeatureFlags["injected"]; ok {
		t.Errorf("Salted's FeatureFlags map shares storage with cfg.FeatureFlags")
	}
}
