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

// The recovery rebuild feeds structured inputs (a list of objects); they must render
// as real HCL, not a stringified Go value.
func TestRenderEnvHCLStructured(t *testing.T) {
	hcl := RenderEnvHCL(map[string]interface{}{
		"s3_existing_buckets": []interface{}{
			map[string]interface{}{"type": "doc", "bucket_name": "x-doc-dr-1"},
			map[string]interface{}{"type": "image", "bucket_name": "x-image-dr-1"},
		},
	})
	want := `s3_existing_buckets = [{ bucket_name = "x-doc-dr-1", type = "doc" }, { bucket_name = "x-image-dr-1", type = "image" }]`
	if !strings.Contains(hcl, want) {
		t.Errorf("structured HCL wrong:\n%s\nwant substring:\n%s", hcl, want)
	}
}
