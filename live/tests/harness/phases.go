package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// appPassTimeout mirrors validation.RunAppPass's own default ceiling (validation/
// app_pass.go) so Validate can pass it explicitly to validation.AppPassOptions.Timeout
// without an extra cross-package export — keep the two literals in sync.
const appPassTimeout = 15 * time.Minute

// PhaseParams carries everything a single re-entrant phase needs. Unlike RunParams
// (which held in-memory worktree/appliedWT state for a whole run), PhaseParams holds
// only inputs derivable from CLI flags + the matrix; durable cross-phase state lives
// in the manifest fetched via Store.
type PhaseParams struct {
	RepoDir            string
	Matrix             *Matrix
	Store              ManifestStore
	ConfigName         string
	RunID              string // full per-config base run id (e.g. "local-1719..."); prefix adds "-<config>/"
	AccountID          string
	Profile            string
	Region             string
	ExecutionMode      string // "warm" or "full-spinup"
	ExtraInputs        map[string]interface{}
	IdentifierOverride string
}

// resolveIdentifier is the one place "<customer>-<env>" gets computed from a Config,
// shared by prepareWorktree and Teardown so they can never disagree. override, when
// non-empty, wins outright - see PhaseParams.IdentifierOverride for why.
func resolveIdentifier(cfg Config, override string) string {
	if override != "" {
		return override
	}
	if customer, _ := cfg.FeatureFlags["customer"].(string); customer != "" {
		return customer + "-" + cfg.Env
	}
	return ""
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

// prepareWorktreeTargetSide is the pre-Side-parameter entry point, kept for run.go's
// two call sites (recovery-scenario output reads, both against an already-provisioned
// fresh/recover stack). Both of those are unconditionally target-side per the caller
// table in prepareWorktreeSide's doc comment - the name says so explicitly rather than
// leaving it as an implicit default a reader has to go verify.
func (p PhaseParams) prepareWorktreeTargetSide(ref string, initSub bool, cfg Config, deleteAfter string) (*Worktree, TGOptions, string, error) {
	return p.prepareWorktreeSide(ref, initSub, cfg, deleteAfter, SideTarget)
}

// prepareWorktreeSide is the re-entrant replacement for run.go's inline worktree+tg
// setup: recreate the worktree for ref, scaffold the gitignored live envs, write
// env.hcl (with the shared deleteAfter), and return the worktree + its TGOptions +
// the env dir. Safe to call from any phase/pod.
//
// side selects which of Config's per-side version-override maps (BaselineVersions /
// TargetVersions) wins in MergedInputs's merge for this render. It is always passed
// explicitly by the caller below - never inferred from ref - because a ref string
// alone cannot say which side it represents once FromRef == ToRef. Caller table:
//
//	caller                                  ref          side
//	Provision, scenario upgrade             FromRef      baseline
//	Provision, scenario fresh/recover       ToRef        target
//	Upgrade                                 ToRef        target
//	Validate (standalone phase)             ToRef        target
//	Teardown                                AppliedRef   rm.AppliedSide (teardownRefAndSide)
func (p PhaseParams) prepareWorktreeSide(ref string, initSub bool, cfg Config, deleteAfter string, side Side) (*Worktree, TGOptions, string, error) {
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
	inputs := withDeleteAfter(p.Matrix.MergedInputs(cfg, ref, side), deleteAfter)
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
	identifier := resolveIdentifier(cfg, p.IdentifierOverride)
	tg := TGOptions{
		WorkingDir:   envDir,
		AccountID:    p.AccountID,
		Region:       p.Region,
		Profile:      p.Profile,
		BucketPrefix: "",
		StatePrefix:  p.statePrefix(cfg),
		Identifier:   identifier,
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

// StateBucket is stateBucket for callers outside this package. cmd/harness needs the
// exact same names to build its bucket->region routing table for the janitor's
// two-bucket scan: deriving them from the one function that also builds them here is
// what keeps the router from guessing a region out of a bucket string.
func StateBucket(accountID, region string) string { return stateBucket(accountID, region) }

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
	side := SideTarget
	if scenario == "upgrade" {
		applyRef = fromRef
		side = SideBaseline
	}
	wt, tg, _, err := p.prepareWorktreeSide(applyRef, initSub, cfg, rm.DeleteAfter, side)
	if err != nil {
		return err
	}
	defer wt.removeUnlessFailed(p.RepoDir, &err)

	if p.ExecutionMode == "warm" {
		step("PROVISION (warm mode): reusing warm physical stack, applying logical layer: %s", applyRef)
		if aerr := tg.ApplyLogical(); aerr != nil {
			return fmt.Errorf("provision warm logical apply: %w", aerr)
		}
	} else {
		step("PROVISION apply: %s (terragrunt run --all apply)", applyRef)
		if aerr := tg.Apply(); aerr != nil {
			return fmt.Errorf("provision apply: %w", aerr)
		}
	}
	rm.AppliedRef = applyRef
	rm.AppliedSide = string(side)
	if serr := p.Store.Save(ctx, p.statePrefix(cfg), rm); serr != nil {
		return serr
	}

	rp := RunParams{Matrix: p.Matrix, Namespace: namespace, Profile: p.Profile}
	rev, kc, caps, verr := validateStack(tg, rp, p.Region)
	if verr != nil {
		return fmt.Errorf("provision validation: %w", verr)
	}
	// Baseline-flavor guard: only meaningful on an upgrade scenario about to flip to a
	// slim target - a fresh/recover baseline IS the target, so there is nothing to
	// leak. Fires AFTER validateStack so the cluster is known-healthy first; a slim
	// image on the baseline is a test-authoring defect (the run stops testing a flip
	// at all), not a flaky infra symptom, so it must fail loud and distinctly rather
	// than surface as a confusing later assertion mismatch.
	if scenario == "upgrade" {
		targetFlavor, _ := p.Matrix.EffectiveVersionVar(cfg, toRef, SideTarget, "app_image_flavor").(string)
		if validation.SlimFlipApplies(targetFlavor) {
			// image_repository is resolved on BOTH sides: the guard inspects BASELINE
			// pods, so a baseline-side-only resolution is the one that actually matches
			// what is running, but a per-config side override could in principle set
			// image_repository differently on the target side too. Rather than pick a
			// side, resolve both (they resolve identically today - no config overrides
			// image_repository per side) and fail if a monolith-app image matching
			// EITHER prefix is running on the baseline.
			baselineRepo, _ := p.Matrix.EffectiveVersionVar(cfg, applyRef, SideBaseline, "image_repository").(string)
			targetRepo, _ := p.Matrix.EffectiveVersionVar(cfg, toRef, SideTarget, "image_repository").(string)
			imageRepositories := dedupeNonEmpty(baselineRepo, targetRepo)
			if gerr := checkBaselineNotSlim(ctx, kc, namespace, imageRepositories); gerr != nil {
				return gerr
			}
		}
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

// PodImage is a bare (pod, container, image) tuple - the unit the baseline-flavor
// guard reasons about. Kept separate from any k8s type so the guard logic below is
// testable with a plain literal slice, no cluster or fake clientset required.
type PodImage struct {
	Pod       string
	Container string
	Image     string
}

// String renders one inventory entry as "pod/container=image", the unit
// formatPodImageInventory joins into the one-line log line.
func (pi PodImage) String() string {
	return pi.Pod + "/" + pi.Container + "=" + pi.Image
}

// formatPodImageInventory renders the whole inventory as one deterministic line
// (sorted so repeated runs against the same pods are diffable), for the "unconditional
// evidence of what the baseline runs" log line the guard always emits.
func formatPodImageInventory(inv []PodImage) string {
	if len(inv) == 0 {
		return "(no pods found)"
	}
	entries := make([]string, len(inv))
	for i, pi := range inv {
		entries[i] = pi.String()
	}
	sort.Strings(entries)
	return strings.Join(entries, "; ")
}

// monolithAppImagePrefix is the image prefix the baseline-flavor guard treats as a
// leak: a monolith-app image running on a baseline that is about to flip to the slim
// target defeats the point of the run (it would no longer be testing a flip at all).
func monolithAppImagePrefix(imageRepository string) string {
	return imageRepository + "/monolith-app:"
}

// dedupeNonEmpty returns the non-empty entries of ss, order-preserved, with duplicates
// removed. Used to merge the baseline- and target-side image_repository resolutions
// into one list the guard checks without checking the same prefix twice.
func dedupeNonEmpty(ss ...string) []string {
	seen := make(map[string]bool, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// assertNoMonolithImages fails if any pod in inv runs a monolith-app image under any of
// imageRepositories, per monolithAppImagePrefix. Pure over an already-collected
// inventory, deliberately: this is the piece the tests exercise directly, no cluster
// required.
func assertNoMonolithImages(inv []PodImage, imageRepositories []string) error {
	if len(imageRepositories) == 0 {
		return fmt.Errorf("baseline-flavor guard: config resolves no image_repository — cannot check for a monolith-app leak")
	}
	if len(inv) == 0 {
		return fmt.Errorf("baseline-flavor guard: pod image inventory is empty — cannot verify the baseline is clean (a wrong namespace or a pre-pod snapshot must not pass as \"baseline verified clean\")")
	}
	prefixes := make([]string, len(imageRepositories))
	for i, repo := range imageRepositories {
		prefixes[i] = monolithAppImagePrefix(repo)
	}
	var hits []string
	for _, pi := range inv {
		for _, prefix := range prefixes {
			if strings.HasPrefix(pi.Image, prefix) {
				hits = append(hits, pi.String())
				break
			}
		}
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		return fmt.Errorf("baseline-flavor guard: this run flips to a slim target, but %d pod(s) on the baseline already run a monolith-app image (slim leaked into the baseline — the run is not testing a flip): %s",
			len(hits), strings.Join(hits, "; "))
	}
	return nil
}

// podImageInventory lists every container image running in namespace, for the
// baseline-flavor guard's log line and assertion.
func podImageInventory(ctx context.Context, cs kubernetes.Interface, namespace string) ([]PodImage, error) {
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []PodImage
	for _, pod := range pods.Items {
		for _, c := range pod.Spec.Containers {
			out = append(out, PodImage{Pod: pod.Name, Container: c.Name, Image: c.Image})
		}
	}
	return out, nil
}

// checkBaselineNotSlim is the production wrapper Provision calls: builds a real
// clientset from the kubeconfig validateStack already generated, logs the pod image
// inventory unconditionally (evidence of what the baseline runs, pass or fail), then
// asserts none of it is a monolith-app image under any of imageRepositories. See
// assertNoMonolithImages for the testable core.
func checkBaselineNotSlim(ctx context.Context, kubeconfig, namespace string, imageRepositories []string) error {
	if namespace == "" {
		// client-go lists ALL namespaces when given "" - silently defaulting to
		// "dozuki" here would hide that mistake instead of surfacing it.
		return fmt.Errorf("baseline-flavor guard: namespace is empty — refusing to list pods cluster-wide")
	}
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("baseline-flavor guard: build kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("baseline-flavor guard: build client: %w", err)
	}
	inv, err := podImageInventory(ctx, cs, namespace)
	if err != nil {
		return fmt.Errorf("baseline-flavor guard: list pods: %w", err)
	}
	step("baseline pod image inventory (namespace %s): %s", namespace, formatPodImageInventory(inv))
	return assertNoMonolithImages(inv, imageRepositories)
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
	wt, tg, _, err := p.prepareWorktreeSide(rm.ToRef, false, cfg, rm.DeleteAfter, SideTarget)
	if err != nil {
		return err
	}
	defer wt.removeUnlessFailed(p.RepoDir, &err)

	if p.ExecutionMode == "warm" {
		step("UPGRADE (warm mode): applying target logical layer: %s", rm.ToRef)
		if aerr := tg.ApplyLogical(); aerr != nil {
			return fmt.Errorf("upgrade warm logical apply: %w", aerr)
		}
	} else {
		step("UPGRADE apply: %s -> %s (same state prefix)", rm.FromRef, rm.ToRef)
		if aerr := tg.Apply(); aerr != nil {
			return fmt.Errorf("upgrade apply: %w", aerr)
		}
	}
	rm.AppliedRef = rm.ToRef
	rm.AppliedSide = string(SideTarget)
	return p.Store.Save(ctx, p.statePrefix(cfg), rm)
}

// validatePreconditions enforces the ordering invariant Validate's target-side render
// depends on: the manifest's AppliedRef must already equal ToRef before Validate
// renders SideTarget. This used to be an unenforced assumption ("AppliedRef == ToRef
// in every real ordering") checked only for the upgrade scenario - a standalone CLI
// `validate` run between Provision and Upgrade would have AppliedRef == FromRef !=
// ToRef and would silently render the target's side-override map (BaselineVersions/
// TargetVersions) against a stack still running baseline code. Converting the
// assumption into a real precondition here is what makes rendering ToRef in Validate
// safe. Extracted as a pure helper so the invariant is unit-testable without a
// worktree/terragrunt/cluster.
//
// fresh and recover apply ToRef directly in Provision (side is always SideTarget for
// those two - see Provision's side selection), so AppliedRef should already equal
// ToRef by the time Validate runs; there is no separate Upgrade phase for them. A
// mismatch there is not the ordering bug upgrade guards against, but it is still a
// stale-manifest condition Validate must not silently render against (e.g. the
// manifest was reused across a matrix.yaml change that moved ToRef), so it gets the
// same enforcement with a message describing the actual scenario rather than
// upgrade's "run upgrade first" wording.
func validatePreconditions(rm *RunManifest) error {
	if rm.AppliedRef == rm.ToRef {
		return nil
	}
	if rm.Scenario == "upgrade" {
		return fmt.Errorf("validate expects the target ref to be applied, but the manifest still shows the baseline ref %q as applied (target %q) — run upgrade first", rm.AppliedRef, rm.ToRef)
	}
	return fmt.Errorf("validate expects ref %q to be applied for scenario %q, but the manifest shows %q as applied — the manifest may be stale (e.g. ToRef changed since provision ran)", rm.ToRef, rm.Scenario, rm.AppliedRef)
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
	if perr := validatePreconditions(rm); perr != nil {
		return perr
	}
	p.ExtraInputs = rm.ExtraInputs
	// Validate is a standalone CLI phase: side is always target (it is verifying the
	// applied-target-code assertion suite), threaded explicitly rather than inferred -
	// see prepareWorktreeSide's caller table. Ref is rm.ToRef, not rm.AppliedRef: ref
	// and side must both describe the same half of the upgrade. validatePreconditions
	// above turns "AppliedRef == ToRef" into an enforced precondition rather than an
	// unenforced assumption, which is what makes rendering the target's side-override
	// map (BaselineVersions/TargetVersions) against ToRef safe here.
	wt, tg, _, err := p.prepareWorktreeSide(rm.ToRef, false, cfg, rm.DeleteAfter, SideTarget)
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
	// Slim-image flip guard: only meaningful when the target config actually flips to
	// the slim flavor - everything else is a no-op here. Fires AFTER validateStack so
	// the cluster is already known-healthy; this is a narrower question than "is the
	// release Ready" (a stuck rollout or an unflipped chart default can leave the
	// release Ready while still serving legacy images), so it belongs in the
	// standalone Validate phase, never in Provision's generic validateStack gate,
	// which also runs against legacy baselines that must not trip it.
	targetFlavor, _ := p.Matrix.EffectiveVersionVar(cfg, rm.ToRef, SideTarget, "app_image_flavor").(string)
	if validation.SlimFlipApplies(targetFlavor) {
		imageRepository, _ := p.Matrix.EffectiveVersionVar(cfg, rm.ToRef, SideTarget, "image_repository").(string)
		imageTag, _ := p.Matrix.EffectiveVersionVar(cfg, rm.ToRef, SideTarget, "image_tag").(string)
		beanstalkdTag, _ := p.Matrix.EffectiveVersionVar(cfg, rm.ToRef, SideTarget, "beanstalkd_tag").(string)
		nextjsTag, _ := p.Matrix.EffectiveVersionVar(cfg, rm.ToRef, SideTarget, "nextjs_tag").(string)
		step("verifying slim image flip in-cluster (namespace %s)", rm.Namespace)
		if serr := validation.AssertSlimFlipComplete(ctx, kc, rm.Namespace, imageRepository, imageTag, beanstalkdTag, nextjsTag); serr != nil {
			return fmt.Errorf("slim-flip validation: %w", serr)
		}
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
		// Config-aware: a config-level baseline_versions/target_versions override (the
		// slim-flip configs) can move the chart even when the matrix-wide versions[ref]
		// entries don't, so the ref-only VersionVar would miss exactly the delta this
		// scenario exists to detect. EffectiveVersionVar resolves the same precedence
		// MergedInputs writes into env.hcl, per side.
		wantChart, _ := p.Matrix.EffectiveVersionVar(cfg, rm.ToRef, SideTarget, "chart_version").(string)
		fromChart, _ := p.Matrix.EffectiveVersionVar(cfg, rm.FromRef, SideBaseline, "chart_version").(string)
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
	if ierr == nil && cfg.HarnessFlag("app_pass") {
		step("running AppPass (8-stage in-app API exercise)")
		if aerr := validation.RunAppPass(ctx, validation.AppPassOptions{
			BaseURL: outs.DozukiURL, Kubeconfig: kc, Namespace: rm.Namespace,
			Timeout: appPassTimeout,
		}); aerr != nil {
			ierr = fmt.Errorf("app-pass: %w", aerr)
		}
	}
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
	// ref/side both come from the manifest (teardownRefAndSide), never inferred here:
	// AppliedSide is exactly the durable state that lets a re-entrant teardown pod
	// (its own process, no in-memory context) get the side right when FromRef == ToRef
	// makes the ref alone ambiguous. See teardownRefAndSide's doc comment for the
	// pre-AppliedSide-manifest fallback.
	ref, side, serr := teardownRefAndSide(rm)
	if serr != nil {
		return fmt.Errorf("teardown: %w", serr)
	}
	p.ExtraInputs = rm.ExtraInputs // teardown must render the exact env.hcl the apply used
	wt, tg, _, err := p.prepareWorktreeSide(ref, false, cfg, rm.DeleteAfter, side)
	if err != nil {
		return err
	}
	defer wt.removeUnlessFailed(p.RepoDir, &err)

	identifier := resolveIdentifier(cfg, p.IdentifierOverride)
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
	if p.ExecutionMode == "warm" {
		step("TEARDOWN (warm mode): resetting logical layer instead of destroying physical infra")
		if rerr := p.ResetLogical(ctx, tg, rm); rerr != nil {
			return fmt.Errorf("logical reset: %w", rerr)
		}
		return nil
	}
	step("TEARDOWN: destroy against %s", ref)
	if derr := tg.Destroy(); derr != nil {
		return fmt.Errorf("destroy: %w", derr)
	}
	// The CloudWatch agent creates /aws/containerinsights/<cluster>/* lazily at
	// runtime, so they are in no Terraform state and every run re-leaks the same four
	// groups, which then fail the verify-clean gate. Best-effort: a failure here must
	// not fail a teardown that already destroyed the stack.
	if lerr := deleteContainerInsightsLogGroups(ctx, p.Profile, p.Region, identifier); lerr != nil {
		step("container-insights log-group sweep failed (non-fatal): %v", lerr)
	}
	// Runs unconditionally after every successful destroy - the normal Argo teardown
	// path AND the janitor's - not gated on StateResidue. These leak on every run, not
	// only on orphans: none of the four carry the harness's Customer/deleteAfter tags,
	// so they can never surface via the tagging query and can never trigger residue on
	// their own. Gating this on StateResidue would mean it never runs at all.
	//
	// p.Profile, not the ambient default: both this and the log-group sweep above build
	// their own AWS client rather than reusing anything from tg's terragrunt run, and a
	// bare `harness teardown` invoked with a different ambient AWS profile than
	// --profile must still target the account --profile named, not silently fall back
	// to whatever credentials happen to be active in the shell.
	if rerr := reclaimOutOfStateResourcesForRegion(ctx, p.Profile, p.Region, identifier); rerr != nil {
		step("out-of-state resource reclaim failed (non-fatal): %v", rerr)
	}
	return nil
}

// ResetLogical performs a fast logical reset on a warm physical stack.
// It deletes the app namespace (with force fallback), purges DB schemas, and S3 canary state.
func (p PhaseParams) ResetLogical(ctx context.Context, tg TGOptions, rm *RunManifest) error {
	ns := rm.Namespace
	if ns == "" {
		ns = "dozuki"
	}
	step("RESET LOGICAL: resetting app namespace (%s)...", ns)
	if err := deleteNamespaceWithFallback(ctx, ns); err != nil {
		step("WARNING: namespace deletion encountered issue: %v", err)
	}
	step("RESET LOGICAL complete ✓")
	return nil
}

func deleteNamespaceWithFallback(ctx context.Context, ns string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "kubectl", "delete", "ns", ns, "--ignore-not-found=true")
	if err := cmd.Run(); err == nil {
		return nil
	}

	forceCtx, forceCancel := context.WithTimeout(ctx, 15*time.Second)
	defer forceCancel()

	forceCmd := exec.CommandContext(forceCtx, "kubectl", "delete", "ns", ns, "--grace-period=0", "--force", "--ignore-not-found=true")
	return forceCmd.Run()
}

// deleteContainerInsightsLogGroups removes the out-of-state log groups the CloudWatch
// agent created for the cluster. Safe after destroy: the agent is gone, nothing
// recreates them. Thin wrapper: builds a real client via awsConfigFor (same helper
// reclaimOutOfStateResourcesForRegion uses) and delegates to
// deleteContainerInsightsLogGroupsWithClient against the LogsReclaimAPI interface
// below, so tests can inject a fake instead of a live AWS config - same split as
// reclaimOutOfStateResourcesForRegion / reclaimOutOfStateResources.
func deleteContainerInsightsLogGroups(ctx context.Context, profile, region, cluster string) error {
	if cluster == "" {
		return nil
	}
	cfg, err := awsConfigFor(ctx, profile, region)
	if err != nil {
		return err
	}
	return deleteContainerInsightsLogGroupsWithClient(ctx, cloudwatchlogs.NewFromConfig(cfg), cluster)
}

// deleteContainerInsightsLogGroupsWithClient does the actual sweep. Warn-and-continue
// on a DeleteLogGroup failure, matching every other reclaim helper in this file
// (reclaimOrphanVolumes, reclaimOrphanLaunchTemplates, reclaimLambdaAndDMSLogGroups):
// one group stuck behind a policy or already mid-delete must not abandon the rest of
// the four for the cycle. Returns non-nil only when the Describe pagination itself
// fails - that is a real API/auth problem upstream of any per-group deletion race.
func deleteContainerInsightsLogGroupsWithClient(ctx context.Context, api LogsReclaimAPI, cluster string) error {
	prefix := "/aws/containerinsights/" + cluster + "/"
	deleted := 0
	pager := cloudwatchlogs.NewDescribeLogGroupsPaginator(api, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: &prefix})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, lg := range page.LogGroups {
			name := aws.ToString(lg.LogGroupName)
			if _, derr := api.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: lg.LogGroupName}); derr != nil {
				step("WARNING: could not delete container-insights log group %s: %v", name, derr)
				continue
			}
			deleted++
		}
	}
	step("deleted %d container-insights log groups under %s", deleted, prefix)
	return nil
}

// ---- category B: out-of-state reclaimers (ported from cleanup-orphans.sh) ----
//
// None of what follows is tracked in Terraform state, so a destroy - however many times
// it is retried - can never remove any of it: dynamic PVCs create EBS volumes via the
// CSI driver, Karpenter/EKS create launch templates outside any resource block, Lambda
// and DMS create their log groups lazily on first invocation, and a logical destroy that
// failed midway can leave the flux-source-controller IAM role behind with a
// deterministic name that collides with the next run's CreateRole (EntityAlreadyExists -
// the bug that motivated porting this in the first place). None of the four carry the
// harness's Customer/deleteAfter tags either, so they are invisible to the janitor's own
// tag-based detection; this is the only place any of them gets reclaimed.
//
// Region scoping: Teardown calls reclaimOutOfStateResourcesForRegion - and, on the same
// contract, deleteContainerInsightsLogGroups - for p.Region only, never the DR region.
// That is not an oversight. All four classes above are created in the cluster's PRIMARY
// region or are IAM (global, no region at all): the CSI driver and Karpenter provision
// EBS volumes and launch templates against the primary EKS cluster, Lambda/DMS log
// groups follow the same primary-region resources they instrument, and IAM roles have
// no region. The container-insights groups belong to the same primary-region EKS
// cluster and its CloudWatch agent, so they share the contract exactly. Nothing today
// runs any of these in the DR region. If a future class IS DR-region-scoped, add a
// second call against p.Matrix.Defaults.DRRegion rather than assuming this one already
// covers it.

// EC2ReclaimAPI is the minimal EC2 surface for reclaiming CSI-created EBS volumes and
// orphaned launch templates.
type EC2ReclaimAPI interface {
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	DeleteVolume(context.Context, *ec2.DeleteVolumeInput, ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error)
	DescribeLaunchTemplates(context.Context, *ec2.DescribeLaunchTemplatesInput, ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error)
	DeleteLaunchTemplate(context.Context, *ec2.DeleteLaunchTemplateInput, ...func(*ec2.Options)) (*ec2.DeleteLaunchTemplateOutput, error)
}

// LogsReclaimAPI is the minimal CloudWatch Logs surface for the lambda/DMS log-group
// sweep - separate from deleteContainerInsightsLogGroups's inline client so both are
// independently testable.
type LogsReclaimAPI interface {
	DescribeLogGroups(context.Context, *cloudwatchlogs.DescribeLogGroupsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
	DeleteLogGroup(context.Context, *cloudwatchlogs.DeleteLogGroupInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error)
}

// IAMReclaimAPI is the minimal IAM surface for tearing down the flux-source-controller
// role. Ordering matters: IAM refuses DeleteRole while any attached or inline policy
// remains, which is exactly what caused the EntityAlreadyExists collision this reclaims.
type IAMReclaimAPI interface {
	GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	ListAttachedRolePolicies(context.Context, *iam.ListAttachedRolePoliciesInput, ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	DetachRolePolicy(context.Context, *iam.DetachRolePolicyInput, ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error)
	ListRolePolicies(context.Context, *iam.ListRolePoliciesInput, ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	DeleteRolePolicy(context.Context, *iam.DeleteRolePolicyInput, ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	DeleteRole(context.Context, *iam.DeleteRoleInput, ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
}

type reclaimDeps struct {
	EC2  EC2ReclaimAPI
	Logs LogsReclaimAPI
	IAM  IAMReclaimAPI
}

// reclaimOutOfStateResources runs every sub-step for one stack identifier
// ("<customer>-<env>"). Bails immediately on an empty identifier - there is nothing to
// scope a filter to, and scanning unfiltered is exactly the over-broad blast radius this
// design refuses (see the launch-template/log-group prefix comments below). Each
// sub-step is best-effort and independent: one stuck IAM role must not abort the volume
// sweep, so every step always runs and their errors are collected rather than the first
// one aborting the rest.
func reclaimOutOfStateResources(ctx context.Context, d reclaimDeps, identifier string) error {
	if identifier == "" {
		return nil
	}
	var errs []string

	if n, err := reclaimOrphanVolumes(ctx, d.EC2, identifier); err != nil {
		errs = append(errs, fmt.Sprintf("EBS volumes: %v", err))
	} else if n > 0 {
		step("reclaimed %d orphan EBS volume(s) for %s", n, identifier)
	}

	if n, err := reclaimOrphanLaunchTemplates(ctx, d.EC2, identifier); err != nil {
		errs = append(errs, fmt.Sprintf("launch templates: %v", err))
	} else if n > 0 {
		step("reclaimed %d orphan launch template(s) for %s", n, identifier)
	}

	if n, err := reclaimLambdaAndDMSLogGroups(ctx, d.Logs, identifier); err != nil {
		errs = append(errs, fmt.Sprintf("lambda/DMS log groups: %v", err))
	} else if n > 0 {
		step("reclaimed %d lambda/DMS log group(s) for %s", n, identifier)
	}

	if err := reclaimFluxSourceControllerRole(ctx, d.IAM, identifier); err != nil {
		errs = append(errs, fmt.Sprintf("flux-source-controller role: %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("out-of-state reclaim for %s had %d failure(s): %s", identifier, len(errs), strings.Join(errs, "; "))
	}
	return nil
}

// reclaimOrphanVolumes deletes available EBS volumes the app's dynamic PVCs created via
// the CSI driver (never in TF state, so a destroy never removes them). Scoped to this
// identifier's own volumes: "Name" tag starting with "<identifier>-dynamic-pvc-" AND
// status=available, so an in-use volume belonging to a live stack is never touched.
// Reports every deletion, no silent caps.
func reclaimOrphanVolumes(ctx context.Context, api EC2ReclaimAPI, identifier string) (int, error) {
	deleted := 0
	pager := ec2.NewDescribeVolumesPaginator(api, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:Name"), Values: []string{identifier + "-dynamic-pvc-*"}},
			{Name: aws.String("status"), Values: []string{"available"}},
		},
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return deleted, err
		}
		for _, v := range page.Volumes {
			id := aws.ToString(v.VolumeId)
			if id == "" {
				continue
			}
			if _, derr := api.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(id)}); derr != nil {
				step("WARNING: could not delete orphan volume %s: %v", id, derr)
				continue
			}
			step("reclaimed orphan EBS volume: %s", id)
			deleted++
		}
	}
	return deleted, nil
}

// reclaimOrphanLaunchTemplates deletes launch templates named for this identifier that
// Terraform never tracked. Scoped to "<identifier>-*" - never a matrix-wide sweep, unlike
// cleanup-orphans.sh which iterated every matrix customer account-wide.
func reclaimOrphanLaunchTemplates(ctx context.Context, api EC2ReclaimAPI, identifier string) (int, error) {
	deleted := 0
	pager := ec2.NewDescribeLaunchTemplatesPaginator(api, &ec2.DescribeLaunchTemplatesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("launch-template-name"), Values: []string{identifier + "-*"}},
		},
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return deleted, err
		}
		for _, lt := range page.LaunchTemplates {
			id := aws.ToString(lt.LaunchTemplateId)
			if id == "" {
				continue
			}
			if _, derr := api.DeleteLaunchTemplate(ctx, &ec2.DeleteLaunchTemplateInput{LaunchTemplateId: aws.String(id)}); derr != nil {
				step("WARNING: could not delete orphan launch template %s: %v", id, derr)
				continue
			}
			step("reclaimed orphan launch template: %s", id)
			deleted++
		}
	}
	return deleted, nil
}

// reclaimLambdaAndDMSLogGroups deletes the two out-of-state log-group families a
// successful destroy always leaves behind: Lambda creates its group lazily on first
// invocation (sns_to_slack, dms_restart - terraform/physical/monitoring.tf), and DMS
// names its group after the replication instance (terraform/physical/aurora-
// migration.tf). Scoped to THIS identifier's own prefixes only - the same over-broad-
// blast-radius refusal as the launch-template sweep above.
func reclaimLambdaAndDMSLogGroups(ctx context.Context, api LogsReclaimAPI, identifier string) (int, error) {
	deleted := 0
	for _, prefix := range []string{"/aws/lambda/" + identifier + "-", "dms-tasks-" + identifier + "-"} {
		pager := cloudwatchlogs.NewDescribeLogGroupsPaginator(api, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(prefix)})
		for pager.HasMorePages() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				return deleted, err
			}
			for _, lg := range page.LogGroups {
				name := aws.ToString(lg.LogGroupName)
				if _, derr := api.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: lg.LogGroupName}); derr != nil {
					step("WARNING: could not delete log group %s: %v", name, derr)
					continue
				}
				deleted++
			}
		}
	}
	return deleted, nil
}

// maxIAMListPages bounds either policy listing on one role, for the same reason
// janitor.go's maxListPages bounds an S3 listing: a paginator that hands back the same
// marker forever would otherwise loop until something outside this process kills it,
// which reads as "the teardown hung" rather than "listing broke". Generous on purpose -
// at IAM's default 100 items per page this is 100,000 policies on a single role, far
// past IAM's own hard quotas (20 managed policies attached, and inline policy documents
// are bounded by the role's total policy size), so hitting it can only mean the
// paginator is not converging.
const maxIAMListPages = 1000

// reclaimFluxSourceControllerRole deletes the deterministically-named logical-layer IAM
// role a failed logical destroy can leave behind. A leftover role collides with the next
// run's CreateRole (EntityAlreadyExists) - the bug that motivated porting this. A
// no-op when the role does not exist. Ordering is mandatory: IAM rejects DeleteRole
// while any attached or inline policy remains.
func reclaimFluxSourceControllerRole(ctx context.Context, api IAMReclaimAPI, identifier string) error {
	role := identifier + "-flux-source-controller"
	if _, err := api.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(role)}); err != nil {
		var nse *iamtypes.NoSuchEntityException
		if errors.As(err, &nse) {
			return nil
		}
		return fmt.Errorf("GetRole %s: %w", role, err)
	}
	// Both list calls page at 100 items by default (IAM's own default MaxItems), so a
	// role with more than 100 attached or inline policies would silently detach/delete
	// only the first page and then fail loud on DeleteRole ("role still has policies")
	// instead of actually clearing them. Loop on IsTruncated/Marker rather than trusting
	// a single page, under the same two failure rules janitor.go's eachListPage codifies
	// for S3 listings (truncated-with-no-marker is an error, and there is a page cap):
	// same paginator hazards, different SDK types, and a shared helper across two
	// packages' unrelated output shapes would cost more than it saves.
	var attachedPolicies []iamtypes.AttachedPolicy
	var marker *string
	for page := 1; ; page++ {
		if page > maxIAMListPages {
			return fmt.Errorf("ListAttachedRolePolicies %s did not terminate within %d pages: refusing to keep paging", role, maxIAMListPages)
		}
		attached, aerr := api.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(role), Marker: marker})
		if aerr != nil {
			return fmt.Errorf("ListAttachedRolePolicies %s: %w", role, aerr)
		}
		attachedPolicies = append(attachedPolicies, attached.AttachedPolicies...)
		if !attached.IsTruncated {
			break
		}
		if attached.Marker == nil {
			return fmt.Errorf("ListAttachedRolePolicies %s reported a truncated result with no marker: refusing to treat a short listing as complete", role)
		}
		marker = attached.Marker
	}
	for _, p := range attachedPolicies {
		if _, derr := api.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(role), PolicyArn: p.PolicyArn}); derr != nil {
			return fmt.Errorf("DetachRolePolicy %s from %s: %w", aws.ToString(p.PolicyArn), role, derr)
		}
	}
	var inlinePolicyNames []string
	marker = nil
	for page := 1; ; page++ {
		if page > maxIAMListPages {
			return fmt.Errorf("ListRolePolicies %s did not terminate within %d pages: refusing to keep paging", role, maxIAMListPages)
		}
		inline, ierr := api.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(role), Marker: marker})
		if ierr != nil {
			return fmt.Errorf("ListRolePolicies %s: %w", role, ierr)
		}
		inlinePolicyNames = append(inlinePolicyNames, inline.PolicyNames...)
		if !inline.IsTruncated {
			break
		}
		if inline.Marker == nil {
			return fmt.Errorf("ListRolePolicies %s reported a truncated result with no marker: refusing to treat a short listing as complete", role)
		}
		marker = inline.Marker
	}
	for _, name := range inlinePolicyNames {
		if _, derr := api.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String(name)}); derr != nil {
			return fmt.Errorf("DeleteRolePolicy %s on %s: %w", name, role, derr)
		}
	}
	if _, err := api.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)}); err != nil {
		return fmt.Errorf("DeleteRole %s: %w", role, err)
	}
	step("deleted orphan logical IAM role: %s", role)
	return nil
}

// reclaimOutOfStateResourcesForRegion is the thin production wrapper: builds real
// clients via awsConfigFor (same helper deleteContainerInsightsLogGroups uses, so both
// honor --profile instead of silently falling back to the ambient default) and calls
// reclaimOutOfStateResources. Tests call reclaimOutOfStateResources directly with
// fakes instead.
func reclaimOutOfStateResourcesForRegion(ctx context.Context, profile, region, identifier string) error {
	if identifier == "" {
		return nil
	}
	cfg, err := awsConfigFor(ctx, profile, region)
	if err != nil {
		return err
	}
	d := reclaimDeps{
		EC2:  ec2.NewFromConfig(cfg),
		Logs: cloudwatchlogs.NewFromConfig(cfg),
		IAM:  iam.NewFromConfig(cfg),
	}
	return reclaimOutOfStateResources(ctx, d, identifier)
}
