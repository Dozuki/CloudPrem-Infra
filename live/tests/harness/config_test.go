package harness

import "testing"

func TestLoadMatrixAndMergeInputs(t *testing.T) {
	m, err := LoadMatrix("testdata/matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	cfg, err := m.Config("min_default")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	base := m.MergedInputs(cfg, "v6.0")
	if base["image_tag"] != "base-app" {
		t.Errorf("image_tag = %v, want base-app", base["image_tag"])
	}
	if base["enable_bi"] != false {
		t.Errorf("enable_bi = %v, want false", base["enable_bi"])
	}
	if _, ok := base["chart_version"]; ok {
		t.Errorf("chart_version should be absent on v6.0")
	}
	tgt := m.MergedInputs(cfg, "v6.1-release")
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
	inputs := m.MergedInputs(full, "v6.0")

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
	newRef := m.MergedInputs(cfg, "v7.1.2")
	if newRef["image_tag"] != "default-app" {
		t.Errorf("v7.1.2 image_tag = %v, want default-app (inherited)", newRef["image_tag"])
	}
	if newRef["chart_version"] != "0.4.1" {
		t.Errorf("v7.1.2 chart_version = %v, want 0.4.1 (inherited)", newRef["chart_version"])
	}

	// A ref-specific key overrides the default; unspecified keys still inherit.
	old := m.MergedInputs(cfg, "v6.0")
	if old["image_tag"] != "old-app" {
		t.Errorf("v6.0 image_tag = %v, want old-app (override)", old["image_tag"])
	}
	if old["chart_version"] != "0.4.1" {
		t.Errorf("v6.0 chart_version = %v, want 0.4.1 (inherited default)", old["chart_version"])
	}

	// VersionVar: ref override wins, else default.
	if got := m.VersionVar("v6.0", "image_tag"); got != "old-app" {
		t.Errorf("VersionVar(v6.0,image_tag) = %v, want old-app", got)
	}
	if got := m.VersionVar("v7.1.2", "chart_version"); got != "0.4.1" {
		t.Errorf("VersionVar(v7.1.2,chart_version) = %v, want 0.4.1", got)
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
