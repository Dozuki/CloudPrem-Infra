package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
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
}

func (p PhaseParams) statePrefix(cfg Config) string {
	return p.RunID + "-" + cfg.Name + "/"
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
	envSub := filepath.Join(p.Matrix.Defaults.EnvPath, cfg.Env)
	envDir := filepath.Join(wt.Dir, envSub)
	if werr := WriteEnvHCL(envDir, withDeleteAfter(p.Matrix.MergedInputs(cfg, ref), deleteAfter)); werr != nil {
		return nil, TGOptions{}, "", werr
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
// ${TG_BUCKET_PREFIX}dozuki-terraform-state-<region>-<account>. The manifest lands
// in the SAME bucket as TF state, under the run's state prefix.
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
	}
	return rm, p.Store.Save(ctx, p.statePrefix(cfg), rm)
}

// Provision applies the baseline (upgrade scenario) or the single ref (fresh
// scenario), validates it, and records baseline state in the manifest. Re-entrant:
// re-running re-applies (terragrunt is convergent) and reuses the manifest.
func (p PhaseParams) Provision(ctx context.Context, scenario, fromRef, toRef, deleteAfter, namespace string) (err error) {
	cfg, err := p.Matrix.Config(p.ConfigName)
	if err != nil {
		return err
	}
	rm, err := p.loadOrInitManifest(ctx, cfg, scenario, fromRef, toRef, deleteAfter)
	if err != nil {
		return err
	}
	rm.Namespace, rm.AccountID = namespace, p.AccountID

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

	step("PROVISION apply: %s (terragrunt run-all apply)", applyRef)
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
	cfg, err := p.Matrix.Config(p.ConfigName)
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
	cfg, err := p.Matrix.Config(p.ConfigName)
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
	if rm.Scenario == "upgrade" {
		wantChart, _ := p.Matrix.VersionVar(rm.ToRef, "chart_version").(string)
		step("verifying upgrade proof (advanced from rev %d; chart %q)", rm.BaselineRev, wantChart)
		if rerr := validation.AssertUpgraded(kc, rm.Namespace, "dozuki", rm.BaselineRev, wantChart); rerr != nil {
			return fmt.Errorf("upgrade proof: %w", rerr)
		}
		return runInfraValidators(ctx, rp, p.Region, caps, outs, true)
	}
	return runInfraValidators(ctx, rp, p.Region, caps, outs, false)
}

// Teardown destroys the run's stack against whichever ref the manifest records as
// applied (essential for cross-architecture upgrades: target code cannot destroy
// baseline state). No manifest → nothing was provisioned → no-op success. Idempotent.
func (p PhaseParams) Teardown(ctx context.Context, keepOnFailure, failed bool) (err error) {
	cfg, err := p.Matrix.Config(p.ConfigName)
	if err != nil {
		return err
	}
	rm, ok, err := p.Store.Load(ctx, p.statePrefix(cfg))
	if err != nil {
		return err
	}
	if !ok {
		step("teardown: no manifest for %s — nothing to destroy", p.statePrefix(cfg))
		return nil
	}
	if failed && keepOnFailure {
		step("teardown SKIPPED (--keep-on-failure): stack for %s left up for debugging", p.statePrefix(cfg))
		return nil
	}
	ref := rm.AppliedRef
	if ref == "" {
		ref = rm.ToRef
	}
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
	return nil
}
