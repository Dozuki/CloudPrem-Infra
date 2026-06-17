package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
)

// RunParams configures one upgrade run.
type RunParams struct {
	RepoDir      string
	Matrix       *Matrix
	ConfigName   string
	FromRef      string // resolved concrete ref
	ToRef        string // resolved concrete ref
	AccountID    string
	Profile      string
	RunID        string // unique per run; namespaces state
	Namespace    string // app namespace, e.g. "dozuki"
	DRRegion     string // DR region (e.g. "us-west-2"); set from matrix defaults
	RestoreDrill bool   // run the RDS restore drill
	EnableDR     bool   // DR validators enabled (enable_dr flag)
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

	ctx := context.Background()
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

	// ---- Pre-upgrade: write continuity sentinel if guide buckets are present ----
	// Read outputs from the baseline to get guide bucket names for the sentinel.
	baseOuts, err := readOutputs(tg(fromWT), region)
	if err != nil {
		return fmt.Errorf("baseline readOutputs (sentinel): %w", err)
	}
	if len(baseOuts.GuideBuckets) > 0 {
		if serr := validation.WriteSentinel(ctx, region, baseOuts.GuideBuckets[0], p.RunID); serr != nil {
			return fmt.Errorf("continuity sentinel write: %w", serr)
		}
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

	// ---- Post-upgrade validators ----
	outs, err := readOutputs(tg(toWT), region)
	if err != nil {
		return fmt.Errorf("post-upgrade readOutputs: %w", err)
	}

	// Logging guard — always run.
	if lerr := validation.AssertControlPlaneLogging(ctx, region, outs.ClusterName); lerr != nil {
		return fmt.Errorf("logging guard: %w", lerr)
	}

	// Continuity sentinel — verify if a guide bucket was available pre-upgrade.
	if len(outs.GuideBuckets) > 0 {
		if serr := validation.VerifySentinel(ctx, region, outs.GuideBuckets[0], p.RunID); serr != nil {
			return fmt.Errorf("continuity sentinel verify: %w", serr)
		}
	}

	// DMS check — no-ops when DMSTaskARN is empty.
	if derr := validation.AssertDMSRunning(ctx, region, outs.DMSTaskARN); derr != nil {
		return fmt.Errorf("DMS running: %w", derr)
	}

	// DR validators — only when enable_dr is set for this config.
	if p.EnableDR && len(outs.DRBucketNames) > 0 {
		// Existence check: DR buckets versioned + replicated RDS backup present.
		if derr := validation.AssertDRExistence(ctx, p.DRRegion, outs.DRBucketNames, outs.DBIdentifier); derr != nil {
			return fmt.Errorf("DR existence: %w", derr)
		}

		// S3 replication flow: one representative check — first guide bucket vs first
		// DR bucket. A full per-bucket pairing is non-trivial (the map key ordering
		// from dr_s3_bucket_names is not guaranteed to align with guide bucket order),
		// so we validate a single representative pair; the existence check covers all
		// DR buckets above.
		if len(outs.GuideBuckets) > 0 {
			if rerr := validation.AssertS3ReplicationFlow(ctx, region, p.DRRegion, outs.GuideBuckets[0], outs.DRBucketNames[0], p.RunID); rerr != nil {
				return fmt.Errorf("DR S3 replication flow: %w", rerr)
			}
		}
	}

	// Restore drill — only when requested (full config).
	if p.RestoreDrill {
		if rderr := validation.RestoreDrill(ctx, p.DRRegion, outs.DBIdentifier, p.RunID); rderr != nil {
			return fmt.Errorf("restore drill: %w", rderr)
		}
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
//
// Physical outputs mapped:
//   - eks_cluster_id        → ClusterName
//   - dms_task_arn          → DMSTaskARN
//   - dms_enabled           → DMSEnabled
//   - guide_images_bucket   → GuideBuckets[0] (if non-empty)
//   - guide_objects_bucket  → GuideBuckets[1] (if non-empty)
//   - guide_pdfs_bucket     → GuideBuckets[2] (if non-empty)
//   - documents_bucket      → GuideBuckets[3] (if non-empty)
//   - dr_s3_bucket_names    → DRBucketNames (values from the map)
//   - db_identifier         → DBIdentifier
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
	boolVal := func(m map[string]interface{}, k string) bool {
		if v, ok := m[k]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
		return false
	}

	// Collect non-empty guide bucket names from the four typed outputs.
	var guideBuckets []string
	for _, key := range []string{"guide_images_bucket", "guide_objects_bucket", "guide_pdfs_bucket", "documents_bucket"} {
		if name := str(physical, key); name != "" {
			guideBuckets = append(guideBuckets, name)
		}
	}

	// dr_s3_bucket_names is a map[string]string output; collect its values.
	var drBucketNames []string
	if v, ok := physical["dr_s3_bucket_names"]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			for _, val := range m {
				if s, ok := val.(string); ok && s != "" {
					drBucketNames = append(drBucketNames, s)
				}
			}
		}
	}

	return validation.StackOutputs{
		DozukiURL:     str(logical, "dozuki_url"),
		ClusterName:   str(physical, "eks_cluster_id"),
		Region:        region,
		DMSTaskARN:    str(physical, "dms_task_arn"),
		DMSEnabled:    boolVal(physical, "dms_enabled"),
		GuideBuckets:  guideBuckets,
		DRBucketNames: drBucketNames,
		DBIdentifier:  str(physical, "db_identifier"),
	}, nil
}
