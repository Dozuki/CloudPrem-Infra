package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestResolveIdentifierPrefersOverride(t *testing.T) {
	cfg := Config{Env: "min", FeatureFlags: map[string]interface{}{"customer": "smokebb"}}
	if got := resolveIdentifier(cfg, "smokeaa-min"); got != "smokeaa-min" {
		t.Fatalf("resolveIdentifier with override = %q, want the override value unchanged", got)
	}
}

func TestResolveIdentifierFallsBackToConfigWhenOverrideEmpty(t *testing.T) {
	cfg := Config{Env: "min", FeatureFlags: map[string]interface{}{"customer": "smokebb"}}
	if got := resolveIdentifier(cfg, ""); got != "smokebb-min" {
		t.Fatalf("resolveIdentifier with no override = %q, want smokebb-min", got)
	}
}

func TestResolveIdentifierEmptyWhenNoCustomerAndNoOverride(t *testing.T) {
	cfg := Config{Env: "min"}
	if got := resolveIdentifier(cfg, ""); got != "" {
		t.Fatalf("resolveIdentifier = %q, want empty (no customer flag, no override)", got)
	}
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

// TestPrepareWorktreeSideThreading proves side actually reaches env.hcl through
// prepareWorktreeSide - the mechanism every phase (Provision/Upgrade/Validate/
// Teardown) relies on for the caller table in prepareWorktreeSide's doc comment. The
// SAME ref is used for both sides, so a pass here rules out the side being inferred
// from the ref string rather than the explicit parameter.
func TestPrepareWorktreeSideThreading(t *testing.T) {
	repo := makeTagRepo(t)
	cfg := Config{
		Name: "flip", Env: "min",
		FeatureFlags:     map[string]interface{}{"customer": "smoke"},
		BaselineVersions: map[string]interface{}{"app_image_flavor": "monolith"},
		TargetVersions:   map[string]interface{}{"app_image_flavor": "slim"},
	}
	m := &Matrix{
		Defaults: Defaults{Region: "us-east-1", EnvPath: "standard/us-east-1"},
		Configs:  []Config{cfg},
	}
	p := PhaseParams{RepoDir: repo, Matrix: m, Store: NewMemStore(), ConfigName: "flip", RunID: "run1", Region: "us-east-1"}

	for _, c := range []struct {
		side Side
		want string
	}{
		{SideBaseline, "monolith"},
		{SideTarget, "slim"},
	} {
		_, _, envDir, err := p.prepareWorktreeSide("v0.0.1", true, cfg, "2026-06-25T00:00:00Z", c.side)
		if err != nil {
			t.Fatalf("prepareWorktreeSide(%s): %v", c.side, err)
		}
		b, rerr := os.ReadFile(filepath.Join(envDir, "env.hcl"))
		if rerr != nil {
			t.Fatalf("read env.hcl: %v", rerr)
		}
		want := `app_image_flavor = "` + c.want + `"`
		if !strings.Contains(string(b), want) {
			t.Errorf("side=%s: env.hcl missing %q\n---\n%s", c.side, want, b)
		}
	}
}

// TestPrepareWorktreeDefaultsToTargetSide covers run.go's two call sites (recovery-
// scenario output reads), which cannot pass Side explicitly because run.go is out of
// this change's scope for editing prepareWorktreeSide's plumbing. The pre-existing
// 4-arg prepareWorktree wrapper must still compile against those call sites AND must
// render as target side - both of run.go's uses are against an already-provisioned
// fresh/recover stack, which is always target per the caller table.
func TestPrepareWorktreeDefaultsToTargetSide(t *testing.T) {
	repo := makeTagRepo(t)
	cfg := Config{
		Name: "flip", Env: "min",
		FeatureFlags:     map[string]interface{}{"customer": "smoke"},
		BaselineVersions: map[string]interface{}{"app_image_flavor": "monolith"},
		TargetVersions:   map[string]interface{}{"app_image_flavor": "slim"},
	}
	m := &Matrix{Defaults: Defaults{Region: "us-east-1", EnvPath: "standard/us-east-1"}, Configs: []Config{cfg}}
	p := PhaseParams{RepoDir: repo, Matrix: m, Store: NewMemStore(), ConfigName: "flip", RunID: "run1", Region: "us-east-1"}

	_, _, envDir, err := p.prepareWorktree("v0.0.1", true, cfg, "2026-06-25T00:00:00Z")
	if err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}
	b, rerr := os.ReadFile(filepath.Join(envDir, "env.hcl"))
	if rerr != nil {
		t.Fatalf("read env.hcl: %v", rerr)
	}
	if !strings.Contains(string(b), `app_image_flavor = "slim"`) {
		t.Errorf("prepareWorktree (legacy wrapper) must default to target side, got:\n%s", b)
	}
}

// ---- Part 3: baseline-flavor guard ----

func TestAssertNoMonolithImagesPassesWhenAbsent(t *testing.T) {
	inv := []PodImage{
		{Pod: "dozuki-app-abc", Container: "app", Image: "069174876992.dkr.ecr.us-east-1.amazonaws.com/slim-app:0.0.0-x"},
		{Pod: "dozuki-nextjs-def", Container: "nextjs", Image: "069174876992.dkr.ecr.us-east-1.amazonaws.com/nextjs:2.23.0"},
	}
	if err := assertNoMonolithImages(inv, "069174876992.dkr.ecr.us-east-1.amazonaws.com"); err != nil {
		t.Fatalf("assertNoMonolithImages = %v, want nil (no monolith-app image present)", err)
	}
}

func TestAssertNoMonolithImagesFiresOnMonolithPod(t *testing.T) {
	repo := "069174876992.dkr.ecr.us-east-1.amazonaws.com"
	inv := []PodImage{
		{Pod: "dozuki-app-abc", Container: "app", Image: repo + "/monolith-app:3625f3c7a3fe9bd3fb87b03a23a4cbb683d44ded.4"},
		{Pod: "dozuki-nextjs-def", Container: "nextjs", Image: repo + "/nextjs:2.23.0"},
	}
	err := assertNoMonolithImages(inv, repo)
	if err == nil {
		t.Fatal("assertNoMonolithImages = nil, want an error (a monolith-app image is present on a baseline about to flip to slim)")
	}
	if !strings.Contains(err.Error(), "dozuki-app-abc") || !strings.Contains(err.Error(), "monolith-app") {
		t.Errorf("error %q does not identify the offending pod/image", err.Error())
	}
}

func TestAssertNoMonolithImagesRequiresImageRepository(t *testing.T) {
	inv := []PodImage{{Pod: "p", Container: "c", Image: "example.com/monolith-app:x"}}
	if err := assertNoMonolithImages(inv, ""); err == nil {
		t.Fatal("assertNoMonolithImages with empty imageRepository = nil, want an error (cannot check for a leak with nothing to check against)")
	}
}

func TestAssertNoMonolithImagesEmptyInventoryPasses(t *testing.T) {
	if err := assertNoMonolithImages(nil, "example.com"); err != nil {
		t.Fatalf("assertNoMonolithImages(nil, ...) = %v, want nil", err)
	}
}

func TestFormatPodImageInventoryIsOneLineAndSorted(t *testing.T) {
	inv := []PodImage{
		{Pod: "z-pod", Container: "app", Image: "img:z"},
		{Pod: "a-pod", Container: "app", Image: "img:a"},
	}
	got := formatPodImageInventory(inv)
	if strings.Contains(got, "\n") {
		t.Errorf("formatPodImageInventory must be one line, got %q", got)
	}
	if strings.Index(got, "a-pod") > strings.Index(got, "z-pod") {
		t.Errorf("formatPodImageInventory not sorted: %q", got)
	}
}
