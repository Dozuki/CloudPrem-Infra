package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ArtifactsDir is the per-run artifacts directory that run.sh bundles + uploads to S3.
func ArtifactsDir(repoDir, runID string) string {
	return filepath.Join(repoDir, "live", "tests", ".artifacts", runID)
}

// captureDiagnostics writes run artifacts (.artifacts/<RUN_ID>/) BEFORE the deferred
// teardown destroys the cluster + removes the worktrees: always the TF inventory, the
// run inputs (env.hcl), and the refs; on failure (full) also a live-cluster dump (pod
// states, events, failed-pod logs, gateway status, rendered configmaps) — the data
// that's gone once Destroy runs. Best-effort: never blocks or fails teardown.
func captureDiagnostics(p RunParams, region, cluster string, full bool, toTG TGOptions, fromEnvHCL, toEnvHCL string) {
	dir := ArtifactsDir(p.RepoDir, p.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, ">> capture: mkdir %s: %v\n", dir, err)
		return
	}

	outcome := "passed"
	if full {
		outcome = "FAILED"
	}
	_ = os.WriteFile(filepath.Join(dir, "refs.txt"), []byte(fmt.Sprintf(
		"run_id=%s\nconfig=%s\nfrom_ref=%s\nto_ref=%s\noutcome=%s\ncaptured=%s\n",
		p.RunID, p.ConfigName, p.FromRef, p.ToRef, outcome, time.Now().Format(time.RFC3339))), 0o644)

	// Run inputs — copied before the worktrees are removed.
	copyFileBestEffort(fromEnvHCL, filepath.Join(dir, "from-env.hcl"))
	copyFileBestEffort(toEnvHCL, filepath.Join(dir, "to-env.hcl"))

	// TF inventory — `state list` has no values; `output` renders sensitive as <sensitive>.
	captureTG(toTG, "physical", dir)
	captureTG(toTG, "logical", dir)

	// Live-cluster dump — only on failure (the high-value, gone-after-teardown data).
	if full && cluster != "" {
		sh := filepath.Join(p.RepoDir, "live", "tests", "capture-cluster.sh")
		cmd := exec.Command("bash", sh, filepath.Join(dir, "cluster"), cluster, region, p.Profile, p.Namespace)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}
	fmt.Fprintf(os.Stderr, "\n>> [harness %s] diagnostics captured -> %s\n", time.Now().Format("15:04:05"), dir)
}

// captureTG dumps `terragrunt state list` + `terragrunt output` for one module (no
// secrets: state list has no values, output redacts sensitive). Best-effort.
func captureTG(o TGOptions, module, dir string) {
	md := filepath.Join(o.WorkingDir, module)
	if _, err := os.Stat(md); err != nil {
		return
	}
	for name, args := range map[string][]string{
		"state-list": {"state", "list"},
		"output":     {"output"},
	} {
		cmd := exec.Command("terragrunt", args...)
		cmd.Dir = md
		cmd.Env = o.env()
		out, _ := cmd.CombinedOutput()
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("tf-%s-%s.txt", module, name)), out, 0o644)
	}
}

func copyFileBestEffort(src, dst string) {
	b, err := os.ReadFile(src)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	_ = os.WriteFile(dst, b, 0o644)
}
