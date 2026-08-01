package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/recovery"
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
	phaseMarks = nil
	runStart = time.Now()
	var cfg Config
	defer func() { printSummary(p, cfg, err) }()

	pp, derr := phaseParamsFromRun(p)
	if derr != nil {
		return derr
	}
	// cfg comes from pp.Config() (not p.Matrix.Config) so the summary's displayed
	// customer matches the salted identifier the phases actually applied.
	if cfg, err = pp.Config(); err != nil {
		return err
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

	// Registered before the teardown defers so it stops LAST: the logical destroy reads
	// Vault too, and this run applies logical twice, so the second one lands near the
	// token's 1h TTL. See vault.go.
	renewCtx, stopRenew := context.WithCancel(context.Background())
	defer stopRenew()
	StartVaultTokenRenewer(renewCtx)

	// Teardown always runs (defer); the signal handler runs it on SIGINT/SIGTERM too.
	// Do NOT discard the teardown error. Swallowing it hid a silent no-op teardown:
	// the stack stayed up, diagnostics were never captured, and the run reported only
	// the original failure — so the leak surfaced later as an unexplained running
	// cluster. run.sh's cleanup backstop still runs either way; this just makes a
	// failed teardown visible in the log instead of invisible.
	defer func() {
		if terr := pp.Teardown(ctx, false, err != nil); terr != nil {
			step("WARNING: teardown failed: %v — stack may still be up; run ./cleanup-orphans.sh %s", terr, p.RunID)
		}
	}()
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
	phaseMarks = nil
	runStart = time.Now()
	var cfg Config
	defer func() { printSummary(p, cfg, err) }()

	pp, derr := phaseParamsFromRun(p)
	if derr != nil {
		return derr
	}
	// cfg comes from pp.Config() (not p.Matrix.Config) so the summary's displayed
	// customer matches the salted identifier the phases actually applied.
	if cfg, err = pp.Config(); err != nil {
		return err
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

	// Registered before the teardown defers so it stops LAST (the logical destroy reads
	// Vault too). See vault.go.
	renewCtx, stopRenew := context.WithCancel(context.Background())
	defer stopRenew()
	StartVaultTokenRenewer(renewCtx)

	// Do NOT discard the teardown error. Swallowing it hid a silent no-op teardown:
	// the stack stayed up, diagnostics were never captured, and the run reported only
	// the original failure — so the leak surfaced later as an unexplained running
	// cluster. run.sh's cleanup backstop still runs either way; this just makes a
	// failed teardown visible in the log instead of invisible.
	defer func() {
		if terr := pp.Teardown(ctx, false, err != nil); terr != nil {
			step("WARNING: teardown failed: %v — stack may still be up; run ./cleanup-orphans.sh %s", terr, p.RunID)
		}
	}()
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

// RunRecovery drills the DECIDED recovery path end to end - the one every database or
// region loss takes (failover-automation design, 2026-07-28): a source stack with DR is
// deployed fresh and validated, its DR secondary is promoted and data-verified by the
// drill, the promoted cluster is snapshotted, and then a NEW stack is stood up in the DR
// REGION seeded from that snapshot plus the replicated *-dr-* buckets - the P3 rebuild,
// executed for real. The rebuilt stack must come healthy AND still hold every heartbeat
// the drill verified on the promoted cluster; only then is the recovery path proven, not
// just the promotion.
//
// p names the SOURCE config (needs enable_dr + restore_drill); recoverConfigName names
// the DR-region rebuild config (its env_path/region point at the DR region). Teardown of
// both stacks and the snapshot always runs, recovery stack first (it adopts the source's
// DR buckets via data sources, so the source teardown - which force_destroys them - must
// come last).
func RunRecovery(p RunParams, recoverConfigName string) (err error) {
	phaseMarks = nil
	runStart = time.Now()

	// pp/pp2 built up front (before either Config() fetch) so both cfg and rcfg salt
	// with the SAME base run id (pp2.RunID == pp.RunID) — salting rcfg with p.RunID
	// here would use the wrong id (p.RunID still carries the source config's suffix)
	// and disagree with what pp2.Provision resolves internally for the same config.
	pp, derr := phaseParamsFromRun(p)
	if derr != nil {
		return derr
	}
	pp2 := pp
	pp2.ConfigName = recoverConfigName

	cfg, err := pp.Config()
	if err != nil {
		return err
	}
	rcfg, err := pp2.Config()
	if err != nil {
		return err
	}
	defer func() { printSummary(p, cfg, err) }()

	// Preflight the ONE cross-region dependency the rebuild cannot work around, before
	// spending ~90 minutes standing up a source stack that would only be torn down. The
	// rebuild region reaches the shared Vault over PrivateLink, and that only works if
	// the service advertises the region.
	rebuildRegion := rcfg.RegionOr(p.Matrix.Defaults.Region)
	if svc, ok := rcfg.FeatureFlags["vault_endpoint_service_name"].(string); ok {
		if err = recovery.CheckEndpointServiceReachable(context.Background(), rebuildRegion, svc); err != nil {
			return err
		}
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

	// Registered before the teardown defers so it stops LAST. This scenario is the one
	// that made the renewer necessary: source stack, drill, snapshot and rebuild run in
	// sequence, so the rebuild's logical apply ALWAYS lands past the token's 1h TTL.
	// See vault.go.
	renewCtx, stopRenew := context.WithCancel(context.Background())
	defer stopRenew()
	StartVaultTokenRenewer(renewCtx)

	// Source-stack teardown: registered FIRST so it runs LAST (the recovery stack
	// adopts the source's DR buckets; destroying them first would strand it).
	defer func() {
		if terr := pp.Teardown(ctx, false, err != nil); terr != nil {
			step("WARNING: source teardown failed: %v — stack may still be up; run ./cleanup-orphans.sh %s", terr, p.RunID)
		}
	}()
	stop := installTeardownOnSignal(func() { _ = pp.Teardown(ctx, false, true) }, os.Exit)
	defer stop()

	// The source stack is an ordinary fresh deploy (scenario "fresh" keeps the
	// greenfield schema check); only the REBUILD runs as scenario "recover", whose
	// restored database must skip that check.
	if err = pp.Provision(ctx, "fresh", "", p.ToRef, deleteAfter, p.Namespace); err != nil {
		return err
	}
	if err = pp.Validate(ctx); err != nil {
		return err
	}

	// The drill has promoted and data-verified the DR secondary; pick its results and
	// the source's DR outputs (replica buckets + their KMS key) out of manifest + state.
	rm, ok, merr := pp.Store.Load(ctx, pp.statePrefix(cfg))
	if merr != nil || !ok {
		return fmt.Errorf("recovery: load source manifest: %w (ok=%v)", merr, ok)
	}
	if rm.PromotedClusterID == "" || rm.DrillHeartbeats == 0 {
		return fmt.Errorf("recovery: the source config must run the promotion drill (enable_dr + restore_drill) — no promoted cluster recorded")
	}
	wt, tg, _, werr := pp.prepareWorktree(rm.AppliedRef, false, cfg, rm.DeleteAfter)
	if werr != nil {
		return werr
	}
	outs, oerr := readOutputs(tg, pp.Region)
	wt.removeUnlessFailed(p.RepoDir, &oerr)
	if oerr != nil {
		return fmt.Errorf("recovery: source outputs: %w", oerr)
	}

	// Sanitized: the run id carries the source config name (recover_source), and RDS
	// rejects underscores in snapshot identifiers - cycle 36 died here.
	snapshotID := recovery.SanitizeSnapshotID("harness-recovery-" + p.RunID)
	step("RECOVERY: snapshotting promoted cluster %s -> %s (%s)", rm.PromotedClusterID, snapshotID, p.DRRegion)
	snapARN, serr := recovery.SnapshotCluster(ctx, p.DRRegion, rm.PromotedClusterID, snapshotID, 30*time.Minute)
	if serr != nil {
		return serr
	}
	defer func() {
		if derr := recovery.DeleteClusterSnapshot(ctx, p.DRRegion, snapshotID); derr != nil {
			step("WARNING: %v — delete it by hand", derr)
		}
	}()

	envInputs, ierr := recovery.Inputs{
		SnapshotARN: snapARN,
		Buckets:     outs.DRBucketByKind,
		S3KMSKeyARN: outs.DRS3KMSKeyARN,
	}.EnvInputs()
	if ierr != nil {
		return ierr
	}

	// The rebuild: a second, independent stack in the DR region, driven through the
	// exact same phases a fresh deploy uses - because the decided recovery IS a fresh
	// deploy with three seeded inputs. Same manifest store; its own state prefix.
	// (pp2 itself was built up front, alongside pp, so cfg/rcfg salt off the same base id.)
	pp2.Region = rcfg.RegionOr(p.Matrix.Defaults.Region)
	pp2.ExtraInputs = envInputs
	defer func() {
		if terr := pp2.Teardown(ctx, false, err != nil); terr != nil {
			step("WARNING: recovery-stack teardown failed: %v — run ./cleanup-orphans.sh %s", terr, p.RunID)
		}
	}()

	step("RECOVERY: rebuilding in %s from snapshot + %d adopted DR buckets", pp2.Region, len(outs.DRBucketByKind))
	if err = pp2.Provision(ctx, "recover", "", p.ToRef, deleteAfter, p.Namespace); err != nil {
		return fmt.Errorf("recovery rebuild: %w", err)
	}
	if err = pp2.Validate(ctx); err != nil {
		return fmt.Errorf("recovery rebuild validation: %w", err)
	}

	// Data survival through the WHOLE path: primary -> replicated secondary ->
	// promotion -> snapshot -> restore -> the rebuilt app's own database. The drill
	// verified the heartbeats on the promoted cluster; the same count must now be
	// readable through the rebuilt stack's app pod.
	rm2, ok2, m2err := pp2.Store.Load(ctx, pp2.statePrefix(rcfg))
	if m2err != nil || !ok2 {
		return fmt.Errorf("recovery: load rebuild manifest: %w (ok=%v)", m2err, ok2)
	}
	wt2, tg2, _, w2err := pp2.prepareWorktree(rm2.AppliedRef, false, rcfg, rm2.DeleteAfter)
	if w2err != nil {
		return w2err
	}
	defer wt2.removeUnlessFailed(p.RepoDir, &err)
	outs2, o2err := readOutputs(tg2, pp2.Region)
	if o2err != nil {
		return fmt.Errorf("recovery: rebuild outputs: %w", o2err)
	}
	kc2, kerr := validation.Kubeconfig(outs2.ClusterName, pp2.Region, p.Profile, filepath.Dir(tg2.WorkingDir))
	if kerr != nil {
		return kerr
	}
	// The heartbeats were written under the SOURCE stack's drill run id.
	drillRunID := pp.statePrefix(cfg)
	got, herr := validation.PrimaryHeartbeatCount(kc2, p.Namespace, drillRunID)
	if herr != nil {
		return fmt.Errorf("recovery: heartbeat read on the rebuilt stack: %w", herr)
	}
	if got != rm.DrillHeartbeats {
		return fmt.Errorf("recovery: DATA LOSS through the rebuild - %d/%d heartbeats survived snapshot+restore", got, rm.DrillHeartbeats)
	}
	step("RECOVERY REBUILD VERIFIED ✓ — %s stack healthy in %s; %d/%d heartbeats survived promote->snapshot->restore; tearing down next",
		recoverConfigName, pp2.Region, got, rm.DrillHeartbeats)
	return nil
}

// runInfraValidators runs the capability-gated infra assertions shared by fresh and
// upgrade runs: control-plane logging, DMS, DR (existence + a representative S3
// replication-flow check), and the optional restore drill. Skips are logged, never
// silent. It (re)probes logging from the live cluster, overriding caps.HasLogging.
// verifyContinuity runs the continuity-sentinel verification (UPGRADE only — a fresh
// deploy writes no baseline sentinel, so callers pass false).
// rm may be nil (callers without durable state); when set, the aurora drill's results
// (promoted cluster id, verified heartbeat count) are recorded on it for later phases —
// the recovery rebuild needs both and neither is discoverable after the drill.
func runInfraValidators(ctx context.Context, p RunParams, region, kubeconfig string, caps Capabilities, outs validation.StackOutputs, verifyContinuity bool, rm *RunManifest) error {
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
		if derr := validation.AssertDRExistence(ctx, p.DRRegion, outs.DRBucketNames, outs); derr != nil {
			return fmt.Errorf("DR existence: %w", derr)
		}

		// S3 replication flow: every source->DR pair, matched by kind. The map key
		// ordering genuinely does not align with guide bucket order — which is why
		// pairing by index put the canary in the image bucket and waited for it in the
		// doc bucket. Keyed pairing makes all four checkable for the same wall-clock.
		if caps.HasGuideBuckets {
			pairs := validation.SourceDRBucketPairs(outs)
			step("DR S3 replication: canarying %d source->DR bucket pair(s)", len(pairs))
			if rerr := validation.AssertS3ReplicationFlowAll(ctx, region, p.DRRegion, pairs, p.RunID); rerr != nil {
				return fmt.Errorf("DR S3 replication flow: %w", rerr)
			}
		}
	} else if p.EnableDR {
		step("skipped: DR (enable_dr set but no DR outputs in %s)", p.ToRef)
	}

	// Restore drill — only when requested (full config), and only on the rds engine.
	//
	// The drill restores a replicated automated backup, which is an RDS-INSTANCE feature.
	// Aurora has no such artifact: its DR is a global cluster whose secondary is promoted,
	// a materially different operation this harness does not implement. Since db_engine
	// now defaults to aurora, that is the common path — so say so out loud rather than
	// attempting it and reporting a confusing "no automated-backup ARN for ''".
	if p.RestoreDrill {
		switch {
		case outs.DRBackupReplicationARN != "":
			step("restore drill: restoring %s from DR backup in %s", outs.DRBackupReplicationARN, p.DRRegion)
			if rderr := validation.RestoreDrill(ctx, p.DRRegion, outs.DRBackupReplicationARN, p.RunID); rderr != nil {
				return fmt.Errorf("restore drill: %w", rderr)
			}
		case outs.AuroraDRGlobalClusterID != "":
			// Promotion is one-way: after this the stack has no DR secondary, so this
			// must stay the LAST validator before teardown (the DR existence check has
			// already run). Teardown afterwards is part of the test - it proves the
			// post-promotion wreckage destroys cleanly.
			step("DR drill: heartbeats -> promote %s in %s -> instance -> in-VPC data probe (~20m)",
				outs.AuroraDRGlobalClusterID, p.DRRegion)
			res, dderr := validation.AuroraPromotionDrill(ctx, validation.DrillParams{
				Kubeconfig:      kubeconfig,
				Namespace:       p.Namespace,
				PrimaryRegion:   region,
				DRRegion:        p.DRRegion,
				GlobalClusterID: outs.AuroraDRGlobalClusterID,
				DBSecretARN:     outs.PrimaryDBSecretARN,
				RunID:           p.RunID,
			})
			if rm != nil {
				rm.PromotedClusterID, rm.DrillHeartbeats = res.PromotedClusterID, res.Heartbeats
			}
			if dderr != nil {
				return fmt.Errorf("aurora promotion drill: %w", dderr)
			}
		default:
			return fmt.Errorf("restore drill requested but the stack emitted no DR database artifact")
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
	// Since CPI v7.19.0 the app is delivered by a Flux HelmRelease applied with
	// kubectl_manifest, which returns as soon as the CR exists — the apply no longer
	// blocks on the install the way helm_release.app's wait=true did. Ask Flux for the
	// verdict instead of guessing a duration: helm-controller knows both when the
	// release is genuinely Ready and, on failure, WHY. That fails in seconds on a broken
	// install (remediation.retries = 0 makes it terminal) instead of waiting out a timer
	// only to report "still not ready". The timeout here is a backstop for a controller
	// that never reconciles, not a readiness estimate.
	//
	// Skipped when there is no HelmRelease — a pre-v7.19.0 baseline in an upgrade run
	// installs through the TF helm provider, where the apply already waited.
	if validation.HelmReleaseManaged(kc, p.Namespace, "dozuki") {
		step("waiting on Flux HelmRelease dozuki (authoritative readiness)")
		if herr := validation.AwaitHelmReleaseReady(kc, p.Namespace, "dozuki", 60*time.Minute); herr != nil {
			return 0, "", caps, herr
		}
	}

	// Ephemeral test stacks must not page anyone: they inherit the production Sentry DSN
	// from the shared Vault global, so boot-order transients (and any bug the harness is
	// deliberately provoking) otherwise land in the real project as alerts. Runs AFTER
	// the release is Ready (patching mid-install would race helm's own rollout watch)
	// and BEFORE the cluster-health wait, which absorbs the resulting re-roll.
	step("silencing Sentry on the app tier (test stacks must not alert production)")
	if serr := validation.SilenceSentry(kc, p.Namespace, "dozuki"); serr != nil {
		return 0, "", caps, serr
	}

	advisory, err := validation.CheckClusterHealth(kc, p.Namespace, critical, 20*time.Minute)
	if err != nil {
		return 0, "", caps, err
	}
	for _, w := range advisory {
		step("advisory: workload %s not Ready (non-critical — not failing the run)", w)
	}

	// Storage invariants from the AZ/PVC review: the default StorageClass must exist and
	// bind WaitForFirstConsumer, and nothing mounting a PVC may sit on spot. Both were
	// true only by accident before; both fail silently in a normal green run when broken.
	step("verifying storage invariants (default SC is WFFC; no PVC pods on spot)")
	if serr := validation.AssertDefaultStorageClassWFFC(kc); serr != nil {
		return 0, "", caps, fmt.Errorf("storage class invariant: %w", serr)
	}
	if serr := validation.AssertNoPVCPodsOnSpot(kc); serr != nil {
		return 0, "", caps, fmt.Errorf("pvc placement invariant: %w", serr)
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
//   - aurora_dr_global_cluster_id  → AuroraDRGlobalClusterID (aurora engine)
//   - aurora_dr_secondary_endpoint → AuroraDRSecondaryHost   (aurora engine)
//   - dr_rds_backup_replication_arn → DRBackupReplicationARN (rds engine)
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

	// Guide buckets, kept BOTH as a flat list (existence checks) and keyed by kind. The
	// kind keys match dr_s3_bucket_names' own keys, which is what makes a source bucket
	// pairable with its DR counterpart — see SourceDRBucketPairs.
	guideBucketKinds := map[string]string{
		"guide_images_bucket":  "image",
		"guide_objects_bucket": "obj",
		"guide_pdfs_bucket":    "pdf",
		"documents_bucket":     "doc",
	}
	var guideBuckets []string
	guideByKind := map[string]string{}
	for _, key := range []string{"guide_images_bucket", "guide_objects_bucket", "guide_pdfs_bucket", "documents_bucket"} {
		if name := str(physical, key); name != "" {
			guideBuckets = append(guideBuckets, name)
			guideByKind[guideBucketKinds[key]] = name
		}
	}

	// dr_s3_bucket_names is a map[string]string output keyed by kind (image/obj/pdf/doc).
	// Keep the keys: dropping them to a bare list is what made the replication check pair
	// buckets by array index, i.e. arbitrarily.
	var drBucketNames []string
	drByKind := map[string]string{}
	if v, ok := physical["dr_s3_bucket_names"]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			for kind, val := range m {
				if s, ok := val.(string); ok && s != "" {
					drBucketNames = append(drBucketNames, s)
					drByKind[kind] = s
				}
			}
		}
	}
	sort.Strings(drBucketNames) // stable ordering for logs; pairing uses the kind map

	return validation.StackOutputs{
		DozukiURL:               str(logical, "dozuki_url"),
		ClusterName:             str(physical, "eks_cluster_id"),
		Region:                  region,
		DMSTaskARN:              str(physical, "dms_task_arn"),
		DMSEnabled:              boolVal(physical, "dms_enabled"),
		GuideBuckets:            guideBuckets,
		GuideBucketByKind:       guideByKind,
		DRBucketNames:           drBucketNames,
		DRBucketByKind:          drByKind,
		DRS3KMSKeyARN:           str(physical, "dr_s3_kms_key_arn"),
		AuroraDRGlobalClusterID: str(physical, "aurora_dr_global_cluster_id"),
		AuroraDRSecondaryHost:   str(physical, "aurora_dr_secondary_endpoint"),
		DRBackupReplicationARN:  str(physical, "dr_rds_backup_replication_arn"),
		PrimaryDBSecretARN:      str(physical, "primary_db_secret"),
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
