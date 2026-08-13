package harness

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Guards the real matrix.yaml, not testdata/. Everything else in this package
// tests against the fixture, which means a new config in the production matrix
// gets no coverage at all until it runs on live AWS.
//
// alertmanager_slack_enabled defaults to true in terraform/logical, so a config
// that omits it inherits the fleet Slack route and pages the shared channel for
// a cluster that exists for an hour. Worse, teardown deletes Alertmanager with
// the cluster, so the resolved notification never sends and the firing message
// outlives the stack. Every smoke stack must opt out, and a comment on one
// config is not enforcement.
func TestAllMatrixConfigsDisableAlertmanagerSlack(t *testing.T) {
	m, err := LoadMatrix("../matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	if len(m.Configs) == 0 {
		t.Fatal("matrix has no configs; the guard below would pass vacuously")
	}
	for _, c := range m.Configs {
		v, ok := c.FeatureFlags["alertmanager_slack_enabled"]
		if !ok {
			t.Errorf("config %q does not set alertmanager_slack_enabled; it will default to true and page the fleet Slack channel", c.Name)
			continue
		}
		if v != false {
			t.Errorf("config %q has alertmanager_slack_enabled = %v, want false", c.Name, v)
		}
	}
}

// TestPostStatusUsesEvidenceSubcommand guards the YAML/Go coupling: nothing else
// keeps argo/20-matrix.yaml's post-status script pointed at `harness evidence` and
// the relay payload carrying log_excerpt. If someone renames or drops the
// subcommand on either side, this is what catches it - the card would otherwise
// silently degrade to today's node summary with no test failure anywhere.
func TestPostStatusUsesEvidenceSubcommand(t *testing.T) {
	b, err := os.ReadFile("../argo/20-matrix.yaml")
	if err != nil {
		t.Fatalf("read 20-matrix.yaml: %v", err)
	}
	script := string(b)

	if !strings.Contains(script, "harness evidence") {
		t.Error("post-status no longer calls `harness evidence`")
	}
	if !strings.Contains(script, `select(.type == "Pod")`) {
		t.Error("post-status dropped the jq fallback expression (requirement 3: evidence must never be the only path)")
	}
	if !strings.Contains(script, "log_excerpt") {
		t.Error("post-status's relay payload no longer carries log_excerpt")
	}
}

// TestSlimMatrixPinsMatchRecordedSnapshot guards matrix.yaml's own promise on
// slim_fresh/slim_upgrade's target_versions comment: "Pins mirror infra-live
// standard/_mcp/dev/us-east-1/slim/env.hcl (dev-slim). Update these when dev-slim's
// env.hcl moves." Nothing enforced that promise - a matrix.yaml edit that changed one
// of these pins (by hand, or by a bad merge) without also bumping dev-slim's env.hcl
// (or vice versa) would drift silently, since both configs are nightly-only and get no
// PR coverage. This test cannot reach infra-live or dev-slim's live env.hcl (no network,
// no cross-repo read - see the CONSTRAINTS this harness runs under), so it is not a
// live drift check against the real source of truth. It is a recorded-snapshot check:
// wantSlimTargetVersions below is the pin set as of the last time someone confirmed
// matrix.yaml matched dev-slim. If this test fails, it means EITHER matrix.yaml moved
// without this snapshot being updated to match, OR (less likely, but just as real) this
// snapshot was hand-edited without checking matrix.yaml still matches dev-slim - either
// way, a human must reconcile the three of matrix.yaml, dev-slim's env.hcl, and this
// snapshot, then update whichever ones are behind.
func TestSlimMatrixPinsMatchRecordedSnapshot(t *testing.T) {
	// Per config, because the two slim configs are no longer required to agree.
	// slim_fresh mirrors dev-slim's env.hcl, which is the original point of this guard.
	// slim_upgrade deliberately LEADS dev-slim on chart_version: it exists to prove an
	// upgrade path before the fleet takes it, so pinning it to whatever dev-slim already
	// runs would mean the harness only ever tests a chart that has already shipped.
	// Everything except chart_version still has to match, so an unnoticed image-tag drift
	// is still a failure.
	wantTargetVersions := map[string]map[string]interface{}{
		"slim_fresh": {
			"app_image_flavor": "slim",
			"chart_version":    "3.1.1",
			"image_tag":        "0.0.0-05d4e70bd52-mpcfix5",
			"beanstalkd_tag":   "6f41576",
			"nextjs_tag":       "0.0.0-f28af33fdb7",
		},
		// 3.3.0 is the first published chart pinning operator 4.2.2, the release that fixed
		// the server-side-apply strategy defect (an install created below operator 4.1.0
		// could not be upgraded past it: `spec.strategy.rollingUpdate: Forbidden`, forever).
		// Move this to dev-slim's value once dev-slim is on 3.3.0 or newer.
		"slim_upgrade": {
			"app_image_flavor": "slim",
			"chart_version":    "3.3.0",
			"image_tag":        "0.0.0-05d4e70bd52-mpcfix5",
			"beanstalkd_tag":   "6f41576",
			"nextjs_tag":       "0.0.0-f28af33fdb7",
		},
	}

	// slim_legacy_appbump is not in the loop above because its whole point is that the two
	// SIDES differ, which a single target-side snapshot cannot express. Assert the property
	// directly instead: both sides pinned, and image_tag the only difference between them.
	// Without this, the config could silently decay into a chart bump or a flavor flip and
	// still look like an app-only test.
	t.Run("slim_legacy_appbump moves only image_tag", func(t *testing.T) {
		m, err := LoadMatrix("../matrix.yaml")
		if err != nil {
			t.Fatalf("LoadMatrix: %v", err)
		}
		cfg, err := m.Config("slim_legacy_appbump")
		if err != nil {
			t.Fatalf("config slim_legacy_appbump: %v", err)
		}
		if len(cfg.BaselineVersions) == 0 || len(cfg.TargetVersions) == 0 {
			t.Fatal("slim_legacy_appbump must pin BOTH sides explicitly; an inherited side lets version_defaults move one of them")
		}
		for _, side := range []struct {
			name string
			vars map[string]interface{}
		}{{"baseline", cfg.BaselineVersions}, {"target", cfg.TargetVersions}} {
			if got := side.vars["chart_version"]; got != "2.10.0" {
				t.Errorf("%s chart_version = %v, want 2.10.0 pinned explicitly", side.name, got)
			}
			if _, ok := side.vars["app_image_flavor"]; ok {
				t.Errorf("%s sets app_image_flavor; this config must stay legacy on both sides", side.name)
			}
		}
		if cfg.BaselineVersions["image_tag"] == cfg.TargetVersions["image_tag"] {
			t.Error("baseline and target image_tag are equal; this config would test nothing")
		}
		// Any key present on one side must be present on the other with an equal value,
		// image_tag excepted - that is the one thing allowed to move.
		for _, pair := range []struct{ a, b map[string]interface{} }{
			{cfg.BaselineVersions, cfg.TargetVersions}, {cfg.TargetVersions, cfg.BaselineVersions},
		} {
			for key, want := range pair.a {
				if key == "image_tag" {
					continue
				}
				got, ok := pair.b[key]
				if !ok {
					t.Errorf("version var %q is set on only one side; the sides must differ in image_tag alone", key)
					continue
				}
				if got != want {
					t.Errorf("version var %q differs across sides (%v vs %v); only image_tag may move", key, want, got)
				}
			}
		}
	})

	m, err := LoadMatrix("../matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	checked := 0
	for _, name := range []string{"slim_fresh", "slim_upgrade"} {
		cfg, err := m.Config(name)
		if err != nil {
			t.Fatalf("config %q: %v", name, err)
		}
		wantSlimTargetVersions := wantTargetVersions[name]
		if wantSlimTargetVersions == nil {
			t.Fatalf("config %q has no recorded snapshot; add one rather than skipping it", name)
		}
		checked++
		if len(cfg.TargetVersions) != len(wantSlimTargetVersions) {
			t.Errorf("config %q: target_versions has %d keys, recorded snapshot has %d — matrix.yaml and this test's snapshot have drifted apart (dev-slim's env.hcl may have moved; update both matrix.yaml and wantSlimTargetVersions to match, or revert whichever one is wrong)",
				name, len(cfg.TargetVersions), len(wantSlimTargetVersions))
			continue
		}
		for key, want := range wantSlimTargetVersions {
			got, ok := cfg.TargetVersions[key]
			if !ok {
				t.Errorf("config %q: target_versions is missing key %q (recorded snapshot expects %v)", name, key, want)
				continue
			}
			if got != want {
				t.Errorf("config %q: target_versions[%q] = %v, recorded snapshot expects %v — matrix.yaml's slim pins moved; if this was a deliberate dev-slim env.hcl bump, update wantSlimTargetVersions in this test to match", name, key, got, want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("neither slim_fresh nor slim_upgrade found in matrix.yaml; this guard checked nothing")
	}
}

// TestMatrixConfigsSaltProducesUniqueCustomer guards Config.Salted's documented no-op
// path (config.go): a base customer already at or past the 10-char terraform cap salts
// to itself. The janitor (harness/janitor.go) already refuses to reason about a candidate
// whose salted customer did not change - it reports needs-review rather than guess - but a
// non-unique identifier is a real collision risk for whatever else derives IAM/Vault/k8s
// resource names from it, not just the janitor. Every config's customer must leave Salted
// room to mutate it.
func TestMatrixConfigsSaltProducesUniqueCustomer(t *testing.T) {
	m, err := LoadMatrix("../matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	for _, c := range m.Configs {
		customer, ok := c.FeatureFlags["customer"].(string)
		if !ok || customer == "" {
			t.Errorf("config %q has no (or a non-string) customer feature flag; the janitor cannot derive an identity for it", c.Name)
			continue
		}
		salted := c.Salted("janitor-invariant-probe")
		got, _ := salted.FeatureFlags["customer"].(string)
		if got == customer {
			t.Errorf("config %q: customer %q is at/past the 10-char salt cap (Salted is a no-op on it); two runs of this config would collide on the same resource identifier", c.Name, customer)
		}
	}
}

// TestJanitorCronMatchesTheGoConstants guards the two Go/YAML couplings the janitor
// depends on and that nothing else enforces. Both are "copy this literal by hand"
// contracts documented in comments on either side, which is exactly the kind of
// agreement that rots silently.
//
//   - schema_version: the notify script asserts the report's version before running its
//     jq pipeline over the field names. If Go bumps JanitorReportSchemaVersion and the
//     script keeps asserting the old number, EVERY cycle posts "cannot read this
//     cycle's report" instead of a report - loud, but permanently wrong.
//   - activeDeadlineSeconds: JanitorPodActiveDeadlineSeconds is the source of truth
//     Sweep's wall-clock budget is derived from. The Resource Reaper execute template,
//     not the report-only scan, must carry it. If the destructive pod's real deadline is
//     shorter than the constant, Sweep budgets time the pod does not have and a destroy
//     gets SIGKILLed mid-run - the one failure the budget exists to prevent.
func TestJanitorCronMatchesTheGoConstants(t *testing.T) {
	b, err := os.ReadFile("../argo/50-janitor-cron.yaml")
	if err != nil {
		t.Fatalf("read 50-janitor-cron.yaml: %v", err)
	}
	yamlText := string(b)

	wantSchema := fmt.Sprintf(`[ "$SCHEMA_VERSION" != "%d" ]`, JanitorReportSchemaVersion)
	if !strings.Contains(yamlText, wantSchema) {
		t.Errorf("the notify script does not assert schema_version %d (looked for %s); Go's JanitorReportSchemaVersion and the script's literal have drifted apart",
			JanitorReportSchemaVersion, wantSchema)
	}

	var templateDoc struct {
		Kind string `yaml:"kind"`
		Spec struct {
			Templates []struct {
				Name                  string `yaml:"name"`
				ActiveDeadlineSeconds int    `yaml:"activeDeadlineSeconds"`
			} `yaml:"templates"`
		} `yaml:"spec"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(yamlText))
	for templateDoc.Kind != "WorkflowTemplate" {
		if err := decoder.Decode(&templateDoc); err != nil {
			t.Fatalf("decode janitor WorkflowTemplate: %v", err)
		}
	}
	executeDeadline := 0
	for _, template := range templateDoc.Spec.Templates {
		if template.Name == "execute" {
			executeDeadline = template.ActiveDeadlineSeconds
			break
		}
	}
	if executeDeadline != JanitorPodActiveDeadlineSeconds {
		t.Errorf("Resource Reaper execute activeDeadlineSeconds=%d, want JanitorPodActiveDeadlineSeconds=%d; the destructive pod must receive the full budget",
			executeDeadline, JanitorPodActiveDeadlineSeconds)
	}

	// The selector contract, in the other direction: these are the exact state strings
	// harness/janitor.go emits, and a rename on the Go side that missed the script
	// would silently drop a whole class of candidate out of Slack.
	for _, state := range []CandidateState{StateOrphan, StateBlocked, StateResidue, StateUnknown, StateNeedsReview} {
		if !strings.Contains(yamlText, fmt.Sprintf(`.state=="%s"`, state)) {
			t.Errorf("the notify script's selectors never mention state %q; candidates in that state would be invisible in Slack", state)
		}
	}
	if !strings.Contains(yamlText, `(.sweep_result // "") != "destroyed"`) {
		t.Error(`the notify script no longer excludes sweep_result "destroyed" from the alarm set; a clean destroy would render under a "teardown FAILED" headline (Sweep leaves State=orphan on purpose)`)
	}
}

// TestSupersedeInvariantsSubmitterSide locks the shape of the supersede step in
// .github/workflows/upgrade-tests.yml. Supersession runs in the SUBMITTER (before
// the new matrix is created, under the harness-pr-submitter identity), not in the
// matrix template - that placement is what removes the patch verb from workload
// pods and makes every same-PR matrix older than the run being submitted by
// construction. Each assertion is a property the consensus review identified as
// load-bearing.
func TestSupersedeInvariantsSubmitterSide(t *testing.T) {
	b, err := os.ReadFile("../../../.github/workflows/upgrade-tests.yml")
	if err != nil {
		t.Fatalf("read upgrade-tests.yml: %v", err)
	}
	y := string(b)

	// The supersede step must run BEFORE the matrix is created: ordering is the
	// substitute for self-exclusion and timestamp tiebreaks.
	supersede := strings.Index(y, "Stop superseded matrix runs for this PR")
	submit := strings.Index(y, "Submit the harness matrix workflow")
	if supersede == -1 {
		t.Fatal("the supersede step is gone from the submitter")
	}
	if submit == -1 {
		t.Fatal("the submit step is gone from the submitter")
	}
	if supersede > submit {
		// Fatal, not Error: the region slice below is meaningless when the order
		// inverts, and slicing it would panic and take the whole binary down.
		t.Fatal("supersede no longer runs before the matrix is created; ordering is its only self-exclusion")
	}

	// The ordering guarantee is only real because the workflow serializes runs
	// per PR: with cancel-in-progress or a changed group key, another run could
	// create a matrix between this run's supersede and its create, and nothing
	// else (no self-exclusion, no tiebreak) would catch it.
	if !strings.Contains(y, "group: upgrade-tests-${{ github.event.pull_request.number || github.run_id }}") {
		t.Error("the per-PR concurrency group changed; supersede-before-create is no longer serialized")
	}
	if !strings.Contains(y, "cancel-in-progress: false") {
		t.Error("cancel-in-progress is no longer false; a cancelled run can leave its matrix orphaned and unsuppressed")
	}

	// A broken supersede must never block the submission it precedes.
	region := y[supersede:submit]
	if !strings.Contains(region, "continue-on-error: true") {
		t.Error("supersede lost continue-on-error; a failure would block the PR's harness run")
	}

	// Graceful stop only: Terminate skips the teardown exit handler and would
	// leak live stacks. The superseded marker must ride the same patch.
	if strings.Contains(region, "Terminate") {
		t.Error("supersede uses Terminate; teardown exit handlers would be skipped")
	}
	if !strings.Contains(region, `"shutdown":"Stop"`) {
		t.Error("supersede no longer patches shutdown: Stop")
	}
	if !strings.Contains(region, `"harness/superseded":"true"`) {
		t.Error("supersede no longer sets the harness/superseded marker; post-status would post false failures for stopped runs")
	}

	// Identity is the label contract, never name parsing.
	if !strings.Contains(region, "harness/trigger=pr,harness/pr=") {
		t.Error("supersede no longer selects by the harness/pr label contract")
	}

	// Children are swept only under successfully patched parents: a marked child
	// beneath an unmarked parent lets the parent post a stale red verdict with no
	// suppression.
	if !strings.Contains(region, "PATCHED") {
		t.Error("supersede lost its PATCHED set; children of a failed parent patch would be stopped while the parent survives unmarked")
	}

	// Bounded API calls; a hung API server must not hang the submit job.
	if !strings.Contains(region, "--request-timeout=10s") {
		t.Error("supersede's kubectl calls lost their request timeout")
	}
}

// TestPostStatusSuppressionInvariants: the matrix's notify handler must suppress
// all external posts for a superseded run, re-check after the slow evidence
// window, and fail OPEN (a normal run's verdict must always post - the pending
// status from submission would otherwise hang the PR head forever).
func TestPostStatusSuppressionInvariants(t *testing.T) {
	b, err := os.ReadFile("../argo/20-matrix.yaml")
	if err != nil {
		t.Fatalf("read 20-matrix.yaml: %v", err)
	}
	y := string(b)
	if !strings.Contains(y, "superseded run; skipping") {
		t.Error("post-status no longer suppresses notifications for superseded runs")
	}
	if !strings.Contains(y, "superseded during evidence collection") {
		t.Error("post-status lost its pre-post re-check; the vault/evidence window would hide a supersession patch")
	}
	if !strings.Contains(y, "posting anyway (fail-open)") {
		t.Error("post-status lost its fail-open path; a kubectl blip would silently swallow a NORMAL run's verdict")
	}
}

// TestSupersedeLabelContractEmitterSide guards the half of the label contract the
// matrix cannot see: the GitHub submitter must keep emitting the harness/pr label,
// or the supersede script takes its "own harness/pr label missing" branch and
// no-ops forever - silently, which is exactly the pre-PR behavior.
func TestSupersedeLabelContractEmitterSide(t *testing.T) {
	b, err := os.ReadFile("../../../.github/workflows/upgrade-tests.yml")
	if err != nil {
		t.Fatalf("read upgrade-tests.yml: %v", err)
	}
	y := string(b)
	if !strings.Contains(y, "PR_NUM:") {
		t.Error("upgrade-tests.yml no longer defines PR_NUM; the submitted matrix loses its supersession identity")
	}
	if !strings.Contains(y, "harness/pr: '${PR_NUM}'") {
		t.Error("upgrade-tests.yml no longer stamps harness/pr on the submitted matrix; supersede selects nothing")
	}
}

// TestMatrixRBACContract locks two facts a reshuffle can silently break. First,
// the janitor cron runs as argo-workflow and pipes `kubectl get workflows` into
// its ownership check; losing that binding's reads does not fail loudly, it makes
// the janitor abort on its G2 empty-body check every night while the cron looks
// green. Second, no template here may name a ServiceAccount other than
// argo-workflow: the argo-privilege-gate admission policy forbids it, and EKS Pod
// Identity is associated with argo-workflow only - a dedicated SA passes today's
// Warn-mode gate, then can never re-apply once the gate enforces, and its
// post-status pod has no AWS identity to log into Vault with.
func TestMatrixRBACContract(t *testing.T) {
	b, err := os.ReadFile("../argo/20-matrix.yaml")
	if err != nil {
		t.Fatalf("read 20-matrix.yaml: %v", err)
	}
	y := string(b)

	role := strings.Index(y, "name: harness-submit-children")
	if role == -1 {
		t.Fatal("harness-submit-children Role is gone; the janitor's workflow listing 403s and the fan-out cannot create children")
	}
	region := y[role:]
	for _, verb := range []string{"'get'", "'list'", "'watch'", "'create'"} {
		if !strings.Contains(region, verb) {
			t.Errorf("harness-submit-children lost verb %s", verb)
		}
	}
	// patch must NOT be on the shared workload SA's Role: it belongs to the GitHub
	// submitter's identity only (30-submitter-rbac.yaml). A patch verb here hands
	// every Terraform-running phase pod the ability to stop or relabel any
	// workflow in the namespace.
	if strings.Contains(region, "'patch'") {
		t.Error("harness-submit-children regained patch; supersession must stay on harness-pr-submitter, off workload pods")
	}
	if !strings.Contains(region, "name: argo-workflow") {
		t.Error("harness-submit-children no longer binds argo-workflow; the janitor cron and the matrix both lose access")
	}

	sub, err := os.ReadFile("../argo/30-submitter-rbac.yaml")
	if err != nil {
		t.Fatalf("read 30-submitter-rbac.yaml: %v", err)
	}
	if !strings.Contains(string(sub), "'patch'") {
		t.Error("harness-pr-submitter lost patch; the submitter cannot stop superseded runs")
	}
	// Every serviceAccountName in the file - workflow-level or per-template - must
	// be argo-workflow. Checking occurrences rather than one literal catches the
	// likelier regression: a single step template quietly naming its own SA.
	saCount := 0
	for _, line := range strings.Split(y, "\n") {
		trimmed := strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(trimmed, "serviceAccountName:"); ok {
			saCount++
			if strings.TrimSpace(name) != "argo-workflow" {
				t.Errorf("a template names ServiceAccount %q; the admission gate rejects it on enforce and pod identity never attaches", strings.TrimSpace(name))
			}
		}
	}
	if saCount == 0 {
		t.Error("no serviceAccountName found; the matrix would fall to the restricted default and lose its workflow RBAC")
	}
	// An unindented kind: line is a top-level object declaration; the indented
	// "- kind: ServiceAccount" inside a RoleBinding's subjects list is fine.
	if strings.HasPrefix(y, "kind: ServiceAccount\n") || strings.Contains(y, "\nkind: ServiceAccount\n") {
		t.Error("20-matrix.yaml defines a ServiceAccount object; only argo-workflow is admitted and pod-identity-backed")
	}
}
