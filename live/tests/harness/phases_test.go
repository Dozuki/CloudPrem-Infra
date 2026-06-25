package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeTagRepo creates a throwaway git repo with a minimal live/generate_live_env.sh
// so generateLiveEnvs + worktree add succeed offline.
func makeTagRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "live"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/usr/bin/env bash\nmkdir -p standard/us-east-1/min\n"
	if err := os.WriteFile(filepath.Join(dir, "live", "generate_live_env.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "init"}, {"tag", "v0.0.1"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestPrepareWorktreeReentrant(t *testing.T) {
	repo := makeTagRepo(t)
	m := &Matrix{
		Defaults:        Defaults{Region: "us-east-1", EnvPath: "standard/us-east-1"},
		VersionDefaults: map[string]interface{}{"image_tag": "x"},
		Configs:         []Config{{Name: "min_default", Env: "min", FeatureFlags: map[string]interface{}{"customer": "smoke"}}},
	}
	p := PhaseParams{RepoDir: repo, Matrix: m, Store: NewMemStore(), ConfigName: "min_default", RunID: "run1", Region: "us-east-1"}
	cfg, _ := m.Config("min_default")

	wt, tg, envDir, err := p.prepareWorktree("v0.0.1", true, cfg, "2026-06-25T00:00:00Z")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if tg.StatePrefix != "run1-min_default/" {
		t.Fatalf("state prefix = %q", tg.StatePrefix)
	}
	if _, err := os.Stat(filepath.Join(envDir, "env.hcl")); err != nil {
		t.Fatalf("env.hcl not written: %v", err)
	}
	// Re-entrancy: calling again on a fresh process-equivalent must not error.
	_ = wt.Remove(repo)
	if _, _, _, err := p.prepareWorktree("v0.0.1", true, cfg, "2026-06-25T00:00:00Z"); err != nil {
		t.Fatalf("second prepare (re-entrant): %v", err)
	}
}
