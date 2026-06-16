package harness

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
)

// RunParams configures one upgrade run.
type RunParams struct {
	RepoDir    string
	Matrix     *Matrix
	ConfigName string
	FromRef    string // resolved concrete ref
	ToRef      string // resolved concrete ref
	AccountID  string
	Profile    string
	RunID      string // unique per run; namespaces state
	Namespace  string // app namespace, e.g. "dozuki"
}

// RunUpgrade executes apply(baseline) -> validate -> apply(target) -> validate
// -> prove -> destroy for one config. Returns the first error; always attempts
// destroy.
func RunUpgrade(p RunParams) (err error) {
	cfg, err := p.Matrix.Config(p.ConfigName)
	if err != nil {
		return err
	}
	if !p.Matrix.VersionProfileExists(p.FromRef) {
		return fmt.Errorf("no version profile for from_ref %q in matrix", p.FromRef)
	}
	if !p.Matrix.VersionProfileExists(p.ToRef) {
		return fmt.Errorf("no version profile for to_ref %q in matrix", p.ToRef)
	}

	base := filepath.Join(p.RepoDir, "live", "tests", "__worktrees__", p.RunID)
	region := p.Matrix.Defaults.Region

	// Baseline worktree initializes the chart git submodule (pre-#145 refs use it).
	fromWT, err := AddWorktree(p.RepoDir, base, p.FromRef, true)
	if err != nil {
		return err
	}
	defer fromWT.Remove(p.RepoDir)
	toWT, err := AddWorktree(p.RepoDir, base, p.ToRef, false)
	if err != nil {
		return err
	}
	defer toWT.Remove(p.RepoDir)

	envSub := filepath.Join(p.Matrix.Defaults.EnvPath, cfg.Env)

	tg := func(wt *Worktree) TGOptions {
		return TGOptions{
			WorkingDir:   filepath.Join(wt.Dir, envSub),
			AccountID:    p.AccountID,
			Region:       region,
			Profile:      p.Profile,
			BucketPrefix: "",
			StatePrefix:  p.RunID + "-" + cfg.Name + "/",
		}
	}

	// Always attempt destroy (against the target/final code) without clobbering
	// an earlier error. Registered last so it runs before the worktree removals.
	defer func() {
		if derr := tg(toWT).Destroy(); derr != nil && err == nil {
			err = fmt.Errorf("destroy: %w", derr)
		}
	}()

	// ---- Baseline apply + validate ----
	if werr := WriteEnvHCL(filepath.Join(fromWT.Dir, envSub), p.Matrix.MergedInputs(cfg, p.FromRef)); werr != nil {
		return werr
	}
	if aerr := tg(fromWT).Apply(); aerr != nil {
		return fmt.Errorf("baseline apply: %w", aerr)
	}
	baselineRev, _, verr := validateStack(tg(fromWT), p, region)
	if verr != nil {
		return fmt.Errorf("baseline validation: %w", verr)
	}

	// ---- Target (upgrade) apply against the SAME state prefix + validate ----
	if werr := WriteEnvHCL(filepath.Join(toWT.Dir, envSub), p.Matrix.MergedInputs(cfg, p.ToRef)); werr != nil {
		return werr
	}
	if aerr := tg(toWT).Apply(); aerr != nil {
		return fmt.Errorf("upgrade apply: %w", aerr)
	}
	_, kc, verr := validateStack(tg(toWT), p, region)
	if verr != nil {
		return fmt.Errorf("upgrade validation: %w", verr)
	}
	wantChart, _ := p.Matrix.Versions[p.ToRef]["chart_version"].(string)
	if rerr := validation.AssertUpgraded(kc, p.Namespace, "dozuki", baselineRev, wantChart); rerr != nil {
		return fmt.Errorf("upgrade proof: %w", rerr)
	}
	return nil
}

// validateStack runs the post-apply assertion suite and returns the helm
// revision and the kubeconfig path it generated (reused by the upgrade proof).
func validateStack(tg TGOptions, p RunParams, region string) (int, string, error) {
	outs, err := readOutputs(tg, region)
	if err != nil {
		return 0, "", err
	}
	if err := validation.CheckEndpoints(outs); err != nil {
		return 0, "", err
	}
	kubeDir := filepath.Dir(tg.WorkingDir)
	kc, err := validation.Kubeconfig(outs.ClusterName, region, p.Profile, kubeDir)
	if err != nil {
		return 0, "", err
	}
	if err := validation.CheckClusterHealth(kc, p.Namespace, 20*time.Minute); err != nil {
		return 0, "", err
	}
	_ = validation.JobSucceeded(kc, p.Namespace, "db-migrations") // best-effort: job may be GC'd
	rev, _ := validation.ReleaseRevision(kc, p.Namespace, "dozuki")
	return rev, kc, nil
}

// readOutputs pulls the outputs the validators need. Names reconciled against
// the real schema: logical "dozuki_url" (app URL) and physical "eks_cluster_id"
// (the cluster name). There is no separate dashboard output, so DashboardURL is
// left empty and CheckEndpoints skips it.
func readOutputs(tg TGOptions, region string) (validation.StackOutputs, error) {
	logical, err := tg.OutputJSON("logical")
	if err != nil {
		return validation.StackOutputs{}, err
	}
	physical, err := tg.OutputJSON("physical")
	if err != nil {
		return validation.StackOutputs{}, err
	}
	str := func(m map[string]interface{}, k string) string {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	return validation.StackOutputs{
		DozukiURL:   str(logical, "dozuki_url"),
		ClusterName: str(physical, "eks_cluster_id"),
		Region:      region,
	}, nil
}
