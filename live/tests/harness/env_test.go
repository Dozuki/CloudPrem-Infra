package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderEnvHCL(t *testing.T) {
	inputs := map[string]interface{}{
		"environment":   "min",
		"enable_bi":     false,
		"rds_multi_az":  false,
		"image_tag":     "abc.1",
		"chart_version": "0.3.0",
		"alarm_email":   "devops@dozuki.com",
		"replica_count": float64(3),
	}
	hcl := RenderEnvHCL(inputs)
	for _, want := range []string{
		`locals {`,
		`environment = "min"`,
		`enable_bi = false`,
		`image_tag = "abc.1"`,
		`chart_version = "0.3.0"`,
		`alarm_email = "devops@dozuki.com"`,
		`replica_count = 3`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("rendered HCL missing %q\n---\n%s", want, hcl)
		}
	}
	dir := t.TempDir()
	envDir := filepath.Join(dir, "min")
	if err := WriteEnvHCL(envDir, inputs); err != nil {
		t.Fatalf("WriteEnvHCL: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envDir, "env.hcl")); err != nil {
		t.Errorf("env.hcl not written: %v", err)
	}
}
