package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

// PhaseParams carries everything a single re-entrant phase needs. Unlike RunParams
// (which held in-memory worktree/appliedWT state for a whole run), PhaseParams holds
// only inputs derivable from CLI flags + the matrix; durable cross-phase state lives
// in the manifest fetched via Store.
type PhaseParams struct {
	RepoDir    string
	Matrix     *Matrix
	Store      ManifestStore
	ConfigName string
	RunID      string // full per-config base run id (e.g. "local-1719..."); prefix adds "-<config>/"
	AccountID  string
	Profile    string
	Region     string
	// ExtraInputs are runtime terraform inputs merged into env.hcl on top of the
	// matrix config (the recovery rebuild's snapshot ARN + adopted buckets). Honored
	// on manifest CREATION and persisted there; later phases re-adopt the manifest's
	// copy so every render — including teardown's — matches the apply.
	ExtraInputs map[string]interface{}
}

func (p PhaseParams) statePrefix(cfg Config) string {
	return p.RunID + "-" + cfg.Name + "/"
}

// Config resolves this phase's Config from the matrix and salts its customer
// feature flag with the run id, so every phase (and every re-entrant retry of
// it, however many separate pods that spans) materializes the identical
// identifier for the same run.
func (p PhaseParams) Config() (Config, error) {
	cfg, err := p.Matrix.Config(p.ConfigName)
	if err != nil {
		return Config{}, err
	}
	return cfg.Salted(p.RunID), nil
}

// prepareWorktree is the re-entrant replacement for run.go's inline worktree+tg
// setup: recreate the worktree for ref, scaffold the gitignored live envs, write
// env.hcl (with the shared deleteAfter), and return the worktree + its TGOptions +
// the env dir. Safe to call from any phase/pod.
func (p PhaseParams) prepareWorktree(ref string, initSub bool, cfg Config, deleteAfter string) (*Worktree, TGOptions, string, error) {
	base := filepath.Join(p.RepoDir, "live", "tests", "__worktrees__", p.RunID)
	wt, err := AddWorktree(p.RepoDir, base, ref, initSub)
	if err != nil {
		return nil, TGOptions{}, "", err
	}
	if gerr := generateLiveEnvs(wt.Dir); gerr != nil {
		return nil, TGOptions{}, "", fmt.Errorf("generate live envs for %s: %w", ref, gerr)
	}
	envSub := filepath.Join(cfg.EnvPathOr(p.Matrix.Defaults.EnvPath), cfg.Env)
	envDir := filepath.Join(wt.Dir, envSub)
	inputs := withDeleteAfter(p.Matrix.MergedInputs(cfg, ref), deleteAfter)
	for k, v := range p.ExtraInputs {
		inputs[k] = v
	}
	if werr := WriteEnvHCL(envDir, inputs); werr != nil {
		return nil, TGOptions{}, "", werr
	}
	// Record which worktree the state now corresponds to, so cleanup-orphans.sh can
	// destroy against the deployed code. Without it the script falls back to the LIVE
	// tree, where find_in_parent_folders blows up before terragrunt runs at all — the
	// destroy silently never happens and a failed run leaks its whole stack (EKS,
	// Aurora, MSK, DMS). writeAppliedMarker existed but had no caller; its unit test
	// calls it directly, so nothing caught that. Best-effort: a marker failure must not
	// fail the run, and a stale marker is still better than the live-tree fallback.
	if merr := writeAppliedMarker(p.RepoDir, p.statePrefix(cfg), envDir); merr != nil {
		step("WARNING: could not write applied-worktree marker (%v) — cleanup-orphans.sh will fall back to the live tree", merr)
	}
	identifier := ""
	if customer, _ := cfg.FeatureFlags["customer"].(string); customer != "" {
		identifier = customer + "-" + cfg.Env
	}
	tg := TGOptions{
		WorkingDir:   envDir,
		AccountID:    p.AccountID,
		Region:       p.Region,
		Profile:      p.Profile,
		BucketPrefix: "",
		StatePrefix:  p.statePrefix(cfg),
		NLBName:      identifier,
	}
	return wt, tg, envDir, nil
}

// stateBucket mirrors live/root.hcl remote_state:
// ${TG_BUCKET_PREFIX}dozuki-terraform-state-<region>-<account>.
//
// The manifest usually lands in the same bucket as the TF state, but NOT always: the
// store is built once from the matrix's default region, while each unit's state follows
// the region that unit deploys to. The recovery scenario's rebuild stack therefore keeps
// its manifest in the PRIMARY bucket while its state lives in the DR one. Anything
// looking for a run's manifest must check both buckets rather than infer it from where
// the state is.
func stateBucket(accountID, region string) string {
	return os.Getenv("TG_BUCKET_PREFIX") + "dozuki-terraform-state-" + region + "-" + accountID
}

// awsConfigFor loads AWS config for an optional shared-config profile + region.
func awsConfigFor(ctx context.Context, profile, region string) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	return config.LoadDefaultConfig(ctx, opts...)
}

// loadOrInitManifest returns the existing manifest for this (run,config) or creates
// a fresh one. deleteAfter is honored ONLY on creation; an existing manifest's value
// (and all prior phase state) is preserved — this is what makes phases re-entrant.
func (p PhaseParams) loadOrInitManifest(ctx context.Context, cfg Config, scenario, fromRef, toRef, deleteAfter string) (*RunManifest, error) {
	if rm, ok, err := p.Store.Load(ctx, p.statePrefix(cfg)); err != nil {
		return nil, err
	} else if ok {
		return rm, nil
	}
	rm := &RunManifest{
		Scenario: scenario, ConfigName: cfg.Name,
		FromRef: fromRef, ToRef: toRef, DeleteAfter: deleteAfter,
		Region: p.Region, DRRegion: p.Matrix.Defaults.DRRegion,
		RestoreDrill: cfg.HarnessFlag("restore_drill"), EnableDR: cfg.HarnessFlag("enable_dr"),
		ExtraInputs: p.ExtraInputs,
	}
	return rm, p.Store.Save(ctx, p.statePrefix(cfg), rm)
}

// Provision applies the baseline (upgrade scenario) or the single ref (fresh
// scenario), validates it, and records baseline state in the manifest. Re-entrant:
// re-running re-applies (terragrunt is convergent) and reuses the manifest.
func (p PhaseParams) Provision(ctx context.Context, scenario, fromRef, toRef, deleteAfter, namespace string) (err error) {
	cfg, err := p.Config()
	if err != nil {
		return err
	}
	rm, err := p.loadOrInitManifest(ctx, cfg, scenario, fromRef, toRef, deleteAfter)
	if err != nil {
		return err
	}
	rm.Namespace, rm.AccountID = namespace, p.AccountID
	p.ExtraInputs = rm.ExtraInputs // a pre-existing manifest's inputs win (re-entrancy)
	// Record the identity THIS phase actually applied with, once. cfg is already
	// Config.Salted(p.RunID) (see PhaseParams.Config), so this is the real customer
	// value the apply below uses - not something the janitor has to recompute later
	// against whatever the matrix happens to say by then. Guarded so a re-entrant retry
	// within the same run never overwrites the value the FIRST successful apply used,
	// even if a later pod's checkout somehow differs.
	if rm.AppliedCustomer == "" {
		if customer, _ := cfg.FeatureFlags["customer"].(string); customer != "" {
			rm.AppliedCustomer = customer
		}
	}

	applyRef := toRef
	initSub := true
	if scenario == "upgrade" {
		applyRef = fromRef
	}
	wt, tg, _, err := p.prepareWorktree(applyRef, initSub, cfg, rm.DeleteAfter)
	if err != nil {
		return err
	}
	defer wt.removeUnlessFailed(p.RepoDir, &err)

	step("PROVISION apply: %s (terragrunt run --all apply)", applyRef)
	if aerr := tg.Apply(); aerr != nil {
		return fmt.Errorf("provision apply: %w", aerr)
	}
	rm.AppliedRef = applyRef
	if serr := p.Store.Save(ctx, p.statePrefix(cfg), rm); serr != nil {
		return serr
	}

	rp := RunParams{Matrix: p.Matrix, Namespace: namespace, Profile: p.Profile}
	rev, _, caps, verr := validateStack(tg, rp, p.Region)
	if verr != nil {
		return fmt.Errorf("provision validation: %w", verr)
	}
	if scenario == "upgrade" {
		rm.BaselineRev = rev
		if serr := p.Store.Save(ctx, p.statePrefix(cfg), rm); serr != nil {
			return serr
		}
		outs, oerr := readOutputs(tg, p.Region)
		if oerr != nil {
			return fmt.Errorf("provision readOutputs (sentinel): %w", oerr)
		}
		if caps.HasGuideBuckets {
			if serr := validation.WriteSentinel(ctx, p.Region, outs.GuideBuckets[0], p.statePrefix(cfg)); serr != nil {
				return fmt.Errorf("continuity sentinel write: %w", serr)
			}
		}
	}
	step("provision validated ✓ (helm revision %d)", rev)
	return nil
}

// Upgrade applies the target ref against the SAME state prefix the baseline used,
// then records ToRef as applied. Requires a prior upgrade-scenario Provision.
func (p PhaseParams) Upgrade(ctx context.Context) (err error) {
	cfg, err := p.Config()
	if err != nil {
		return err
	}
	rm, ok, err := p.Store.Load(ctx, p.statePrefix(cfg))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no manifest for %s — run provision first", p.statePrefix(cfg))
	}
	if rm.Scenario == "fresh" {
		return fmt.Errorf("upgrade phase invalid for fresh scenario (run %s)", p.statePrefix(cfg))
	}
	p.ExtraInputs = rm.ExtraInputs
	wt, tg, _, err := p.prepareWorktree(rm.ToRef, false, cfg, rm.DeleteAfter)
	if err != nil {
		return err
	}
	defer wt.removeUnlessFailed(p.RepoDir, &err)

	step("UPGRADE apply: %s -> %s (same state prefix)", rm.FromRef, rm.ToRef)
	if aerr := tg.Apply(); aerr != nil {
		return fmt.Errorf("upgrade apply: %w", aerr)
	}
	rm.AppliedRef = rm.ToRef
	return p.Store.Save(ctx, p.statePrefix(cfg), rm)
}

// Validate runs the post-apply assertion suite against the currently-applied ref.
// For upgrades it also proves the helm revision advanced past the manifest's
// recorded BaselineRev and verifies the continuity sentinel; for fresh it skips
// both. Re-entrant: reads everything it needs from the manifest + live outputs.
func (p PhaseParams) Validate(ctx context.Context) (err error) {
	cfg, err := p.Config()
	if err != nil {
		return err
	}
	rm, ok, err := p.Store.Load(ctx, p.statePrefix(cfg))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no manifest for %s — run provision first", p.statePrefix(cfg))
	}
	p.ExtraInputs = rm.ExtraInputs
	wt, tg, _, err := p.prepareWorktree(rm.AppliedRef, false, cfg, rm.DeleteAfter)
	if err != nil {
		return err
	}
	defer wt.removeUnlessFailed(p.RepoDir, &err)

	rp := RunParams{
		Matrix: p.Matrix, Namespace: rm.Namespace, Profile: p.Profile,
		RunID: p.statePrefix(cfg), DRRegion: rm.DRRegion,
		RestoreDrill: rm.RestoreDrill, EnableDR: rm.EnableDR, ToRef: rm.ToRef,
	}
	rev, kc, caps, verr := validateStack(tg, rp, p.Region)
	if verr != nil {
		return fmt.Errorf("validation: %w", verr)
	}
	if rev < 1 {
		return fmt.Errorf("helm release not deployed (revision %d)", rev)
	}
	outs, oerr := readOutputs(tg, p.Region)
	if oerr != nil {
		return fmt.Errorf("readOutputs: %w", oerr)
	}

	// Feature-tier assertions, gated on the config's own flags. These are named-exact
	// rather than glob-matched: if enable_webhooks/enable_bi is set but the release
	// never rendered those workloads, that is a failure, not a silent pass. Deliberately
	// after the generic cluster-health gate so the app is already up and the only thing
	// still settling is the feature tier itself.
	if cfg.HarnessFlag("enable_bi") {
		step("verifying BI tier in-cluster (grafana + db bootstrap)")
		if berr := validation.AssertBIHealthy(kc, rm.Namespace, 15*time.Minute); berr != nil {
			return fmt.Errorf("bi validation: %w", berr)
		}
	}

	// Greenfield only: compare each seeded database against the schema dump in the
	// deployed image. The migration Job reporting Complete does not mean the schema is
	// complete — a rejected statement truncates the import and the retry then skips the
	// whole block and exits 0. Nothing else in this run asks that question.
	if rm.Scenario == "fresh" {
		step("verifying greenfield schema matches the image's dumps")
		if serr := validation.AssertFreshSchemaComplete(kc, rm.Namespace); serr != nil {
			return fmt.Errorf("schema validation: %w", serr)
		}
	}

	verifyContinuity := false
	if rm.Scenario == "upgrade" {
		wantChart, _ := p.Matrix.VersionVar(rm.ToRef, "chart_version").(string)
		fromChart, _ := p.Matrix.VersionVar(rm.FromRef, "chart_version").(string)
		// A ref delta with no chart delta (a docs-only or infra-only PR) upgrades
		// nothing through Flux, so the release revision stays put. Only demand an
		// advance when the chart version actually moved between the refs.
		mustAdvance := fromChart != wantChart
		step("verifying upgrade proof (from rev %d; chart %q; advance required: %t)", rm.BaselineRev, wantChart, mustAdvance)
		if rerr := validation.AssertUpgraded(kc, rm.Namespace, "dozuki", rm.BaselineRev, wantChart, mustAdvance); rerr != nil {
			return fmt.Errorf("upgrade proof: %w", rerr)
		}
		verifyContinuity = true
	}
	// The drill records its results on rm even when it errors; persist them either way
	// so a recovery phase (or a rescue by hand) knows what the drill left behind.
	ierr := runInfraValidators(ctx, rp, p.Region, kc, caps, outs, verifyContinuity, rm)
	if rm.PromotedClusterID != "" {
		if serr := p.Store.Save(ctx, p.statePrefix(cfg), rm); serr != nil && ierr == nil {
			ierr = serr
		}
	}
	return ierr
}

// Teardown destroys the run's stack against whichever ref the manifest records as
// applied (essential for cross-architecture upgrades: target code cannot destroy
// baseline state). No manifest → nothing was provisioned → no-op success. Idempotent.
func (p PhaseParams) Teardown(ctx context.Context, keepOnFailure, failed bool) (err error) {
	cfg, err := p.Config()
	if err != nil {
		return err
	}
	rm, ok, err := p.Store.Load(ctx, p.statePrefix(cfg))
	if err != nil {
		// Log before returning. This path used to be silent, so a manifest read failure
		// looked identical to a teardown that never ran at all.
		step("teardown: could not load manifest for %s: %v — NOT destroying; run ./cleanup-orphans.sh %s", p.statePrefix(cfg), err, p.RunID)
		return err
	}
	if !ok {
		step("teardown: no manifest for %s — nothing to destroy", p.statePrefix(cfg))
		return nil
	}
	// Defect P1 fix: persist the keep-on-failure decision to the manifest BEFORE acting
	// on it, on every call, whatever it is. The manifest outlives the Workflow CR (Argo
	// TTLs a failed one after three days - 10-scenario.yaml ttlStrategy), so this is the
	// one place the decision to leave a stack up on purpose still lives once the CR is
	// gone and the janitor's workflow-index lookup goes blank. The skip decision two
	// lines down does not itself depend on this save succeeding (it reads the local
	// keepOnFailure argument, not rm) - only a LATER read (the janitor) does - but when
	// this call is about to skip a destroy specifically because keepOnFailure is true,
	// losing that write would silently reopen the exact hole this fix closes for this
	// run's whole remaining lifetime, so that combination fails loud rather than best-
	// effort. Every other combination (nothing protective is riding on it) logs and
	// moves on, same posture as writeAppliedMarker elsewhere in this function.
	if rm.KeepOnFailure != keepOnFailure || !rm.KeepOnFailureRecorded {
		rm.KeepOnFailure = keepOnFailure
		rm.KeepOnFailureRecorded = true
		if serr := p.Store.Save(ctx, p.statePrefix(cfg), rm); serr != nil {
			if failed && keepOnFailure {
				return fmt.Errorf("teardown: could not durably record --keep-on-failure before skipping the destroy: %w", serr)
			}
			step("WARNING: could not record keep-on-failure to the manifest (%v) — a later janitor read may not see this run's choice", serr)
		}
	}
	if failed && keepOnFailure {
		step("teardown SKIPPED (--keep-on-failure): stack for %s left up for debugging", p.statePrefix(cfg))
		return nil
	}
	ref := rm.AppliedRef
	if ref == "" {
		ref = rm.ToRef
	}
	p.ExtraInputs = rm.ExtraInputs // teardown must render the exact env.hcl the apply used
	wt, tg, _, err := p.prepareWorktree(ref, false, cfg, rm.DeleteAfter)
	if err != nil {
		return err
	}
	defer wt.removeUnlessFailed(p.RepoDir, &err)

	identifier := ""
	if customer, _ := cfg.FeatureFlags["customer"].(string); customer != "" {
		identifier = customer + "-" + cfg.Env
	}
	// captureDiagnostics reads RepoDir/RunID/ConfigName/FromRef/ToRef/Profile/Namespace
	// off RunParams; `identifier` ("<customer>-<env>") IS the EKS cluster name (run.go).
	// RunID drives the .artifacts/<id> dir — use the per-config id WITHOUT trailing slash.
	rp := RunParams{
		RepoDir: p.RepoDir, RunID: strings.TrimSuffix(p.statePrefix(cfg), "/"),
		ConfigName: cfg.Name, FromRef: rm.FromRef, ToRef: rm.ToRef,
		Profile: p.Profile, Namespace: rm.Namespace, Matrix: p.Matrix,
	}
	step("capturing diagnostics -> .artifacts/%s (full=%v)", rp.RunID, failed)
	captureDiagnostics(rp, p.Region, identifier, failed, tg, tg.WorkingDir, "")
	step("TEARDOWN: destroy against %s", ref)
	if derr := tg.Destroy(); derr != nil {
		return fmt.Errorf("destroy: %w", derr)
	}
	// The CloudWatch agent creates /aws/containerinsights/<cluster>/* lazily at
	// runtime, so they are in no Terraform state and every run re-leaks the same four
	// groups, which then fail the verify-clean gate. Best-effort: a failure here must
	// not fail a teardown that already destroyed the stack.
	if lerr := deleteContainerInsightsLogGroups(ctx, p.Region, identifier); lerr != nil {
		step("container-insights log-group sweep failed (non-fatal): %v", lerr)
	}
	return nil
}

// deleteContainerInsightsLogGroups removes the out-of-state log groups the CloudWatch
// agent created for the cluster. Safe after destroy: the agent is gone, nothing
// recreates them.
func deleteContainerInsightsLogGroups(ctx context.Context, region, cluster string) error {
	if cluster == "" {
		return nil
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return err
	}
	cw := cloudwatchlogs.NewFromConfig(cfg)
	prefix := "/aws/containerinsights/" + cluster + "/"
	deleted := 0
	pager := cloudwatchlogs.NewDescribeLogGroupsPaginator(cw, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: &prefix})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, lg := range page.LogGroups {
			if _, err := cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: lg.LogGroupName}); err != nil {
				return err
			}
			deleted++
		}
	}
	step("deleted %d container-insights log groups under %s", deleted, prefix)
	return nil
}
