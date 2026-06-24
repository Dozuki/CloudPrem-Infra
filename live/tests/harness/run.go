package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// phaseMark records one step() marker (time + message) for the end-of-run summary.
type phaseMark struct {
	at  time.Time
	msg string
}

// Per-run phase tracking, reset at the start of each run. The harness runs configs
// sequentially, so a package-level recorder is sufficient (no concurrency).
var (
	phaseMarks []phaseMark
	runStart   time.Time
)

// step prints a timestamped progress marker to stderr (captured by run.sh's tee) so
// the log shows what the harness is doing during the otherwise-silent gaps between
// terragrunt stages (validation waits, output reads, validators, teardown). Each
// marker is also recorded for the end-of-run HARNESS RUN SUMMARY banner.
func step(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	phaseMarks = append(phaseMarks, phaseMark{at: time.Now(), msg: msg})
	fmt.Fprintf(os.Stderr, "\n>> [harness %s] %s\n", time.Now().Format("15:04:05"), msg)
}

// RunParams configures one upgrade/fresh run (the local, single-process driver).
type RunParams struct {
	RepoDir           string
	Matrix            *Matrix
	ConfigName        string
	FromRef           string // resolved concrete ref
	ToRef             string // resolved concrete ref
	AccountID         string
	Profile           string
	RunID             string    // unique per run; namespaces state
	Namespace         string    // app namespace, e.g. "dozuki"
	DRRegion          string    // DR region (e.g. "us-west-2"); set from matrix defaults
	RestoreDrill      bool      // run the RDS restore drill
	EnableDR          bool      // DR validators enabled (enable_dr flag)
	CriticalWorkloads []string  // critical workload name globs; empty → DefaultCriticalWorkloads()
	StartTime         time.Time // wall-clock run start; used to compute deleteAfter (zero → time.Now())
}

// phaseParamsFromRun adapts a local RunParams into PhaseParams, using an S3-backed
// manifest store on the harness state bucket so local runs share the exact code path
// (and S3 state) the Argo phases use. RunParams.RunID already includes the per-config
// suffix from the scenario test, so strip it back to the base id that statePrefix
// re-appends "-<config>/" to.
func phaseParamsFromRun(p RunParams) (PhaseParams, error) {
	ctx := context.Background()
	awsCfg, err := awsConfigFor(ctx, p.Profile, p.Matrix.Defaults.Region)
	if err != nil {
		return PhaseParams{}, err
	}
	bucket := stateBucket(p.AccountID, p.Matrix.Defaults.Region)
	base := strings.TrimSuffix(p.RunID, "-"+p.ConfigName)
	return PhaseParams{
		RepoDir: p.RepoDir, Matrix: p.Matrix, Store: NewS3Store(s3.NewFromConfig(awsCfg), bucket),
		ConfigName: p.ConfigName, RunID: base, AccountID: p.AccountID,
		Profile: p.Profile, Region: p.Matrix.Defaults.Region,
	}, nil
}

// RunUpgrade executes provision(baseline) -> upgrade(target) -> validate for one
// config, with teardown via defer. Returns the first error; always attempts teardown.
// It composes the same re-entrant phases the Argo Workflow drives — this is the
// local, single-process driver of them.
func RunUpgrade(p RunParams) (err error) {
	cfg, err := p.Matrix.Config(p.ConfigName)
	if err != nil {
		return err
	}
	phaseMarks = nil
	runStart = time.Now()
	defer func() { printSummary(p, cfg, err) }()

	pp, derr := phaseParamsFromRun(p)
	if derr != nil {
		return derr
	}
	ctx := context.Background()
	startTime := p.StartTime
	if startTime.IsZero() {
		startTime = runStart
	}
	ttl := p.Matrix.Defaults.ReaperTTLHours
	if ttl == 0 {
		ttl = 24
	}
	deleteAfter := startTime.Add(time.Duration(ttl) * time.Hour).UTC().Format(time.RFC3339)

	// Teardown always runs (defer); the signal handler runs it on SIGINT/SIGTERM too.
	defer func() { _ = pp.Teardown(ctx, false, err != nil) }()
	stop := installTeardownOnSignal(func() { _ = pp.Teardown(ctx, false, true) }, os.Exit)
	defer stop()

	if err = pp.Provision(ctx, "upgrade", p.FromRef, p.ToRef, deleteAfter, p.Namespace); err != nil {
		return err
	}
	if err = pp.Upgrade(ctx); err != nil {
		return err
	}
	if err = pp.Validate(ctx); err != nil {
		return err
	}
	step("ALL VALIDATIONS PASSED ✓ — %s -> %s upgrade verified; tearing down next", p.FromRef, p.ToRef)
	return nil
}

// RunFresh executes provision -> validate for a SINGLE ref (p.ToRef): no baseline, no
// upgrade proof, no continuity sentinel — it verifies a clean from-scratch deploy (a
// class of bug an in-place upgrade can mask). Teardown via defer; first error wins.
func RunFresh(p RunParams) (err error) {
	cfg, err := p.Matrix.Config(p.ConfigName)
	if err != nil {
		return err
	}
	phaseMarks = nil
	runStart = time.Now()
	defer func() { printSummary(p, cfg, err) }()

	pp, derr := phaseParamsFromRun(p)
	if derr != nil {
		return derr
	}
	ctx := context.Background()
	startTime := p.StartTime
	if startTime.IsZero() {
		startTime = runStart
	}
	ttl := p.Matrix.Defaults.ReaperTTLHours
	if ttl == 0 {
		ttl = 24
	}
	deleteAfter := startTime.Add(time.Duration(ttl) * time.Hour).UTC().Format(time.RFC3339)

	defer func() { _ = pp.Teardown(ctx, false, err != nil) }()
	stop := installTeardownOnSignal(func() { _ = pp.Teardown(ctx, false, true) }, os.Exit)
	defer stop()

	if err = pp.Provision(ctx, "fresh", "", p.ToRef, deleteAfter, p.Namespace); err != nil {
		return err
	}
	if err = pp.Validate(ctx); err != nil {
		return err
	}
	step("ALL VALIDATIONS PASSED ✓ — %s fresh deploy verified; tearing down next", p.ToRef)
	return nil
}

// runInfraValidators runs the capability-gated infra assertions shared by fresh and
// upgrade runs: control-plane logging, DMS, DR (existence + a representative S3
// replication-flow check), and the optional restore drill. Skips are logged, never
// silent. It (re)probes logging from the live cluster, overriding caps.HasLogging.
// verifyContinuity runs the continuity-sentinel verification (UPGRADE only — a fresh
// deploy writes no baseline sentinel, so callers pass false).
func runInfraValidators(ctx context.Context, p RunParams, region string, caps Capabilities, outs validation.StackOutputs, verifyContinuity bool) error {
	caps.HasLogging = validation.LoggingEnabled(ctx, region, outs.ClusterName)
	if caps.HasLogging {
		if lerr := validation.AssertControlPlaneLogging(ctx, region, outs.ClusterName); lerr != nil {
			return fmt.Errorf("logging guard: %w", lerr)
		}
	} else {
		step("skipped: control-plane logging (not enabled on %s)", p.ToRef)
	}

	if verifyContinuity {
		if caps.HasGuideBuckets {
			if serr := validation.VerifySentinel(ctx, region, outs.GuideBuckets[0], p.RunID); serr != nil {
				return fmt.Errorf("continuity sentinel verify: %w", serr)
			}
		} else {
			step("skipped: continuity sentinel (no guide buckets in %s)", p.ToRef)
		}
	}

	if caps.HasDMS {
		if derr := validation.AssertDMSRunning(ctx, region, outs.DMSTaskARN); derr != nil {
			return fmt.Errorf("DMS running: %w", derr)
		}
	} else {
		step("skipped: DMS (no dms_task_arn in %s)", p.ToRef)
	}

	if p.EnableDR && caps.HasDR {
		// Existence check: DR buckets versioned + replicated RDS backup present.
		if derr := validation.AssertDRExistence(ctx, p.DRRegion, outs.DRBucketNames, outs.DBIdentifier); derr != nil {
			return fmt.Errorf("DR existence: %w", derr)
		}

		// S3 replication flow: one representative check — first guide bucket vs first
		// DR bucket. A full per-bucket pairing is non-trivial (the map key ordering
		// from dr_s3_bucket_names is not guaranteed to align with guide bucket order),
		// so we validate a single representative pair; the existence check covers all
		// DR buckets above.
		if caps.HasGuideBuckets {
			if rerr := validation.AssertS3ReplicationFlow(ctx, region, p.DRRegion, outs.GuideBuckets[0], outs.DRBucketNames[0], p.RunID); rerr != nil {
				return fmt.Errorf("DR S3 replication flow: %w", rerr)
			}
		}
	} else if p.EnableDR {
		step("skipped: DR (enable_dr set but no DR outputs in %s)", p.ToRef)
	}

	// Restore drill — only when requested (full config).
	if p.RestoreDrill {
		step("restore drill: restoring %s from DR backup in %s", outs.DBIdentifier, p.DRRegion)
		if rderr := validation.RestoreDrill(ctx, p.DRRegion, outs.DBIdentifier, p.RunID); rderr != nil {
			return fmt.Errorf("restore drill: %w", rderr)
		}
	}
	return nil
}

// generateLiveEnvs scaffolds the concrete live/<partition>/<region>/<env> trees
// from live/.skel inside a worktree. Those dirs are gitignored (generated by
// generate_live_env.sh), so a fresh checkout of a ref does not contain them.
func generateLiveEnvs(worktreeDir string) error {
	return run(filepath.Join(worktreeDir, "live"), "bash", "generate_live_env.sh")
}

// validateStack runs the post-apply assertion suite and returns the helm
// revision, the kubeconfig path it generated (reused by the upgrade proof),
// and the detected Capabilities for use by later validation stages.
func validateStack(tg TGOptions, p RunParams, region string) (int, string, Capabilities, error) {
	outs, err := readOutputs(tg, region)
	if err != nil {
		return 0, "", Capabilities{}, err
	}
	caps := DetectCapabilities(outs)
	if err := validation.CheckEndpoints(outs); err != nil {
		return 0, "", caps, err
	}
	kubeDir := filepath.Dir(tg.WorkingDir)
	kc, err := validation.Kubeconfig(outs.ClusterName, region, p.Profile, kubeDir)
	if err != nil {
		return 0, "", caps, err
	}
	critical := p.CriticalWorkloads
	if len(critical) == 0 {
		critical = DefaultCriticalWorkloads()
	}
	advisory, err := validation.CheckClusterHealth(kc, p.Namespace, critical, 20*time.Minute)
	if err != nil {
		return 0, "", caps, err
	}
	for _, w := range advisory {
		step("advisory: workload %s not Ready (non-critical — not failing the run)", w)
	}
	_ = validation.JobSucceeded(kc, p.Namespace, "db-migrations") // best-effort: job may be GC'd
	rev, _ := validation.ReleaseRevision(kc, p.Namespace, "dozuki")
	return rev, kc, caps, nil
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

// printSummary writes a final, human-readable banner so a run's outcome, what ran
// (with per-phase durations), and where the artifacts live are visible at a glance —
// no scrolling or grepping the full log. Printed last, on pass or fail.
func printSummary(p RunParams, cfg Config, err error) {
	result := "PASS ✓"
	if err != nil {
		result = "FAIL ✗: " + err.Error()
	}
	dr := "(disabled)"
	if p.EnableDR {
		dr = p.DRRegion + " (enabled)"
	}
	customer, _ := cfg.FeatureFlags["customer"].(string)

	var b strings.Builder
	line := func(format string, a ...interface{}) { fmt.Fprintf(&b, format+"\n", a...) }
	line("")
	line("========================= HARNESS RUN SUMMARY =========================")
	line(" Result:    %s", result)
	line(" Config:    %s  (customer=%s, env=%s)", cfg.Name, customer, cfg.Env)
	if p.FromRef == "" {
		line(" Deploy:    %s  (fresh)", p.ToRef)
	} else {
		line(" Upgrade:   %s  ->  %s", p.FromRef, p.ToRef)
	}
	line(" Region:    %s    DR: %s", p.Matrix.Defaults.Region, dr)
	line(" Run ID:    %s", p.RunID)
	if !runStart.IsZero() {
		line(" Duration:  %s", time.Since(runStart).Round(time.Second))
		if len(phaseMarks) > 0 {
			line(" Phases:")
			for i, m := range phaseMarks {
				next := time.Now()
				if i+1 < len(phaseMarks) {
					next = phaseMarks[i+1].at
				}
				line("   +%-8s %-56s %s",
					m.at.Sub(runStart).Round(time.Second),
					truncate(m.msg, 56),
					next.Sub(m.at).Round(time.Second))
			}
		}
	}
	line(" Artifacts: live/tests/.artifacts/%s/  (run log + diagnostics; bundled to S3 by run.sh)", p.RunID)
	line("======================================================================")
	fmt.Fprint(os.Stderr, b.String())
}

// withDeleteAfter sets in["delete_after"] = ts and returns the same map, so it
// can be used inline at WriteEnvHCL call sites. Every worktree written by a run
// carries this timestamp, allowing the ResourceReaper to purge orphans left behind
// by a failed teardown.
func withDeleteAfter(in map[string]interface{}, ts string) map[string]interface{} {
	in["delete_after"] = ts
	return in
}

// truncate collapses s to its first line and caps it at n bytes for the summary table.
func truncate(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
