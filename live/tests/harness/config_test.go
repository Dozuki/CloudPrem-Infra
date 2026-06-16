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
