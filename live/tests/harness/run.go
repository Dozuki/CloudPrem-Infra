package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
)

// phaseMark records one step() marker (time + message) for the end-of-run summary.
type phaseMark struct {
	at  time.Time
	msg string
}

// Per-run phase tracking, reset at the start of each RunUpgrade. The harness runs
// configs sequentially, so a package-level recorder is sufficient (no concurrency).
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

// RunParams configures one upgrade run.
type RunParams struct {
	RepoDir           string
	Matrix            *Matrix
	ConfigName        string
	FromRef           string // resolved concrete ref
	ToRef             string // resolved concrete ref
	AccountID         string
	Profile           string
	RunID             string   // unique per run; namespaces state
	Namespace         string   // app namespace, e.g. "dozuki"
	DRRegion          string   // DR region (e.g. "us-west-2"); set from matrix defaults
	RestoreDrill      bool     // run the RDS restore drill
	EnableDR          bool     // DR validators enabled (enable_dr flag)
	CriticalWorkloads []string // critical workload name globs; empty → DefaultCriticalWorkloads()
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

	// Track phases and print a final summary banner on the way out. Registered before
	// the worktree/teardown defers so it runs LAST — the run's outcome, what ran (with
	// per-phase durations), and artifact location at a glance, no log-grepping needed.
	phaseMarks = nil
	runStart = time.Now()
	defer func() { printSummary(p, cfg, err) }()

	ctx := context.Background()
	base := filepath.Join(p.RepoDir, "live", "tests", "__worktrees__", p.RunID)
	region := p.Matrix.Defaults.Region

	// Refresh remote-tracking refs so branch refs (e.g. v6.1-release) are checked out
	// at their pushed state, not a stale local branch.
	FetchOrigin(p.RepoDir)

	step("preparing worktrees: %s (baseline) + %s (upgrade)", p.FromRef, p.ToRef)
	// Baseline worktree initializes the chart git submodule (pre-#145 refs use it).
	fromWT, err := AddWorktree(p.RepoDir, base, p.FromRef, true)
	if err != nil {
		return err
	}
	defer fromWT.removeUnlessFailed(p.RepoDir, &err)
	toWT, err := AddWorktree(p.RepoDir, base, p.ToRef, false)
	if err != nil {
		return err
	}
	defer toWT.removeUnlessFailed(p.RepoDir, &err)

	// The concrete live/<partition>/<region>/<env> trees are gitignored (generated
	// from live/.skel by generate_live_env.sh), so a fresh worktree doesn't contain
	// them. Scaffold them in each worktree before writing env.hcl / applying.
	for _, wt := range []*Worktree{fromWT, toWT} {
		if gerr := generateLiveEnvs(wt.Dir); gerr != nil {
			return fmt.Errorf("generate live envs for %s: %w", wt.Ref, gerr)
		}
	}

	envSub := filepath.Join(p.Matrix.Defaults.EnvPath, cfg.Env)
	fromEnvHCL := filepath.Join(fromWT.Dir, envSub, "env.hcl")
	toEnvHCL := filepath.Join(toWT.Dir, envSub, "env.hcl")

	// terraform local.identifier = "<customer>-<env>" — the name of both the NLB
	// (teardown clears its deletion protection) and the EKS cluster (diagnostics dump).
	identifier := ""
	if customer, _ := cfg.FeatureFlags["customer"].(string); customer != "" {
		identifier = customer + "-" + cfg.Env
	}

	tg := func(wt *Worktree) TGOptions {
		return TGOptions{
			WorkingDir:   filepath.Join(wt.Dir, envSub),
			AccountID:    p.AccountID,
			Region:       region,
			Profile:      p.Profile,
			BucketPrefix: "",
			StatePrefix:  p.RunID + "-" + cfg.Name + "/",
			NLBName:      identifier,
		}
	}

	// Write env.hcl into BOTH worktrees up front so the deferred destroy (which
	// runs against the applied worktree, appliedWT) always has a valid config to
	// clean up with, even if the baseline apply fails before the target env.hcl
	// would otherwise be written.
	if werr := WriteEnvHCL(filepath.Join(fromWT.Dir, envSub), p.Matrix.MergedInputs(cfg, p.FromRef)); werr != nil {
		return werr
	}
	if werr := WriteEnvHCL(filepath.Join(toWT.Dir, envSub), p.Matrix.MergedInputs(cfg, p.ToRef)); werr != nil {
		return werr
	}

	// Track the worktree whose code matches what is deployed: baseline applies
	// first, so default fromWT; flips to toWT only after the upgrade apply succeeds.
	// The teardown destroys against THIS worktree (not always toWT) — essential for
	// cross-architecture upgrades (e.g. v5.3->v6.1) where target code cannot destroy
	// baseline state.
	var appliedWT atomic.Pointer[Worktree]
	appliedWT.Store(fromWT)
	// Marker so the out-of-process cleanup-orphans backstop can destroy against the
	// matching worktree too. Written for the baseline up front (a baseline-apply
	// interrupt still leaves a correct from_ref marker).
	_ = writeAppliedMarker(p.RepoDir, tg(fromWT).StatePrefix, tg(fromWT).WorkingDir)

	teardown, stopSig := setupTeardown(p, region, identifier, &appliedWT, tg, fromEnvHCL, toEnvHCL, &err)
	defer teardown(false)
	defer stopSig()

	// ---- Baseline apply + validate ----
	step("BASELINE apply: %s (terragrunt run-all apply — physical then logical)", p.FromRef)
	if aerr := tg(fromWT).Apply(); aerr != nil {
		return fmt.Errorf("baseline apply: %w", aerr)
	}
	step("baseline applied — validating: cluster health (waits for pods Ready, up to 20m), endpoints, helm release")
	baselineRev, _, baseCaps, verr := validateStack(tg(fromWT), p, region)
	if verr != nil {
		return fmt.Errorf("baseline validation: %w", verr)
	}
	step("baseline validated ✓ (helm revision %d)", baselineRev)

	// ---- Pre-upgrade: write continuity sentinel if guide buckets are present ----
	// Read outputs from the baseline to get guide bucket names for the sentinel.
	baseOuts, err := readOutputs(tg(fromWT), region)
	if err != nil {
		return fmt.Errorf("baseline readOutputs (sentinel): %w", err)
	}
	if baseCaps.HasGuideBuckets {
		if serr := validation.WriteSentinel(ctx, region, baseOuts.GuideBuckets[0], p.RunID); serr != nil {
			return fmt.Errorf("continuity sentinel write: %w", serr)
		}
	}

	// ---- Target (upgrade) apply against the SAME state prefix + validate ----
	if werr := WriteEnvHCL(filepath.Join(toWT.Dir, envSub), p.Matrix.MergedInputs(cfg, p.ToRef)); werr != nil {
		return werr
	}
	step("UPGRADE apply: %s -> %s (same state prefix; terragrunt run-all apply)", p.FromRef, p.ToRef)
	if aerr := tg(toWT).Apply(); aerr != nil {
		return fmt.Errorf("upgrade apply: %w", aerr)
	}
	// Upgrade applied: target code now matches the deployed state.
	appliedWT.Store(toWT)
	_ = writeAppliedMarker(p.RepoDir, tg(toWT).StatePrefix, tg(toWT).WorkingDir)
	step("upgrade applied — validating: cluster health (up to 20m), endpoints, helm release")
	_, kc, upCaps, verr := validateStack(tg(toWT), p, region)
	if verr != nil {
		return fmt.Errorf("upgrade validation: %w", verr)
	}
	wantChart, _ := p.Matrix.Versions[p.ToRef]["chart_version"].(string)
	step("verifying upgrade proof (helm revision advanced from %d; chart %q)", baselineRev, wantChart)
	if rerr := validation.AssertUpgraded(kc, p.Namespace, "dozuki", baselineRev, wantChart); rerr != nil {
		return fmt.Errorf("upgrade proof: %w", rerr)
	}

	// Post-upgrade validators — gated by detected capabilities; skips are logged.
	step("upgrade proven ✓ — capability-gated post-upgrade validators")
	outs, err := readOutputs(tg(toWT), region)
	if err != nil {
		return fmt.Errorf("post-upgrade readOutputs: %w", err)
	}
	if verr := runInfraValidators(ctx, p, region, upCaps, outs, true); verr != nil {
		return verr
	}

	step("ALL VALIDATIONS PASSED ✓ — %s -> %s upgrade verified; tearing down next", p.FromRef, p.ToRef)
	return nil
}

// RunFresh executes apply -> validate -> capability-gated infra validators ->
// destroy for a SINGLE ref (p.ToRef). No baseline, no continuity sentinel, and no
// upgrade proof — it verifies a clean from-scratch deploy of one release (a class
// of bug that an in-place upgrade can mask). Returns the first error; always
// attempts destroy.
func RunFresh(p RunParams) (err error) {
	cfg, err := p.Matrix.Config(p.ConfigName)
	if err != nil {
		return err
	}
	if !p.Matrix.VersionProfileExists(p.ToRef) {
		return fmt.Errorf("no version profile for to_ref %q in matrix", p.ToRef)
	}

	phaseMarks = nil
	runStart = time.Now()
	defer func() { printSummary(p, cfg, err) }()

	ctx := context.Background()
	base := filepath.Join(p.RepoDir, "live", "tests", "__worktrees__", p.RunID)
	region := p.Matrix.Defaults.Region

	FetchOrigin(p.RepoDir)

	step("preparing worktree: %s (fresh deploy)", p.ToRef)
	wt, err := AddWorktree(p.RepoDir, base, p.ToRef, true)
	if err != nil {
		return err
	}
	defer wt.removeUnlessFailed(p.RepoDir, &err)

	if gerr := generateLiveEnvs(wt.Dir); gerr != nil {
		return fmt.Errorf("generate live envs for %s: %w", wt.Ref, gerr)
	}

	envSub := filepath.Join(p.Matrix.Defaults.EnvPath, cfg.Env)
	envHCL := filepath.Join(wt.Dir, envSub, "env.hcl")

	identifier := ""
	if customer, _ := cfg.FeatureFlags["customer"].(string); customer != "" {
		identifier = customer + "-" + cfg.Env
	}

	tg := func(w *Worktree) TGOptions {
		return TGOptions{
			WorkingDir:   filepath.Join(w.Dir, envSub),
			AccountID:    p.AccountID,
			Region:       region,
			Profile:      p.Profile,
			BucketPrefix: "",
			StatePrefix:  p.RunID + "-" + cfg.Name + "/",
			NLBName:      identifier,
		}
	}

	if werr := WriteEnvHCL(filepath.Join(wt.Dir, envSub), p.Matrix.MergedInputs(cfg, p.ToRef)); werr != nil {
		return werr
	}

	var appliedWT atomic.Pointer[Worktree]
	appliedWT.Store(wt)
	_ = writeAppliedMarker(p.RepoDir, tg(wt).StatePrefix, tg(wt).WorkingDir)
	teardown, stopSig := setupTeardown(p, region, identifier, &appliedWT, tg, envHCL, "", &err)
	defer teardown(false)
	defer stopSig()

	step("FRESH apply: %s (terragrunt run-all apply — physical then logical)", p.ToRef)
	if aerr := tg(wt).Apply(); aerr != nil {
		return fmt.Errorf("fresh apply: %w", aerr)
	}
	step("applied — validating: cluster health (waits for pods Ready, up to 20m), endpoints, helm release")
	rev, _, caps, verr := validateStack(tg(wt), p, region)
	if verr != nil {
		return fmt.Errorf("fresh validation: %w", verr)
	}
	if rev < 1 {
		return fmt.Errorf("helm release %q not deployed (revision %d)", "dozuki", rev)
	}
	step("fresh deploy validated ✓ (helm revision %d) — capability-gated infra validators", rev)

	outs, err := readOutputs(tg(wt), region)
	if err != nil {
		return fmt.Errorf("fresh readOutputs: %w", err)
	}
	if ierr := runInfraValidators(ctx, p, region, caps, outs, false); ierr != nil {
		return ierr
	}

	step("ALL VALIDATIONS PASSED ✓ — %s fresh deploy verified; tearing down next", p.ToRef)
	return nil
}

// setupTeardown builds the sync.Once teardown closure (capture diagnostics, then
// destroy against the APPLIED worktree) and installs the SIGINT/SIGTERM handler.
// Shared by RunUpgrade and RunFresh. errp points at the caller's named return so a
// destroy error can surface; it is read ONLY on the non-interrupted path — the
// signal path exits via os.Exit while the main goroutine is still running, so
// reading err there would race.
func setupTeardown(p RunParams, region, identifier string, appliedWT *atomic.Pointer[Worktree], tg func(*Worktree) TGOptions, fromEnvHCL, toEnvHCL string, errp *error) (teardown func(bool), stop func()) {
	var once sync.Once
	teardown = func(interrupted bool) {
		once.Do(func() {
			wt := appliedWT.Load()
			failed := interrupted
			if !interrupted {
				failed = *errp != nil
			}
			step("capturing diagnostics -> .artifacts/%s (full=%v)", p.RunID, failed)
			captureDiagnostics(p, region, identifier, failed, tg(wt), fromEnvHCL, toEnvHCL)
			step("TEARDOWN: destroy against %s (logical best-effort, then ALWAYS physical)", wt.Ref)
			derr := tg(wt).Destroy()
			if !interrupted && derr != nil && *errp == nil {
				*errp = fmt.Errorf("destroy: %w", derr)
			}
			if !failed {
				removeAppliedMarker(p.RepoDir, tg(wt).StatePrefix)
			}
		})
	}
	stop = installTeardownOnSignal(func() { teardown(true) }, os.Exit)
	return teardown, stop
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
