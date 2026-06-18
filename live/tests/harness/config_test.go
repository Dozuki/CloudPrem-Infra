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
