package harness

import (
	"os"
	"strconv"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// scenarioWorkflowPath is argo/10-scenario.yaml relative to this package's test working
// directory (go test always runs with cwd = the package dir, and argo/ is live/tests'
// sole child directory named argo, a sibling of harness/).
const scenarioWorkflowPath = "../argo/10-scenario.yaml"

// argoScenarioWorkflow is the minimal shape TestDeadlineInvariant needs out of
// 10-scenario.yaml's WorkflowTemplate — just enough of spec.templates[].steps to reach
// each step's "deadline" argument. Deliberately not a full Argo Workflow schema: this
// test's only job is reading the one number the consensus-panel fix depends on, not
// validating the workflow shape (that's Argo's own linting, out of scope here).
type argoScenarioWorkflow struct {
	Spec struct {
		Templates []struct {
			Name  string `yaml:"name"`
			Steps [][]struct {
				Name      string `yaml:"name"`
				Arguments struct {
					Parameters []struct {
						Name  string `yaml:"name"`
						Value string `yaml:"value"`
					} `yaml:"parameters"`
				} `yaml:"arguments"`
			} `yaml:"steps"`
		} `yaml:"templates"`
	} `yaml:"spec"`
}

// scenarioStepDeadlines parses 10-scenario.yaml and returns step name -> its "deadline"
// argument, in seconds, for the "scenario" template's steps. Values are Argo string
// literals like '9000' (never a live {{...}} expression — every phase step's deadline is
// a plain number, per 10-scenario.yaml itself), so strconv.Atoi is enough; a step whose
// deadline argument is missing, non-numeric, or templated is simply absent from the
// returned map, and the caller decides whether that is fatal for the step it needed.
func scenarioStepDeadlines(t *testing.T, path string) map[string]int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var wf argoScenarioWorkflow
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	deadlines := map[string]int{}
	for _, tmpl := range wf.Spec.Templates {
		if tmpl.Name != "scenario" {
			continue
		}
		for _, group := range tmpl.Steps {
			for _, step := range group {
				for _, param := range step.Arguments.Parameters {
					if param.Name != "deadline" {
						continue
					}
					if secs, err := strconv.Atoi(param.Value); err == nil {
						deadlines[step.Name] = secs
					}
				}
			}
		}
	}
	return deadlines
}

// TestDeadlineInvariant is the static half of the consensus-panel fix (see phases.go's
// hrPodDeadlineEnv doc comment for the runtime half, clampHRWaitBudget). It PARSES
// argo/10-scenario.yaml directly — the deadline numbers are never duplicated in Go — and
// asserts, for every step that runs validateStack's HelmRelease wait, that
//
//	desired_wait_budget + hrWaitReserve + hrPodStartupSlack  <=  that step's Argo pod deadline
//
// hrPodStartupSlack is included here even though it approximates a clock-skew concern
// (pod start vs process start) rather than real post-wait work like hrWaitReserve: the
// static check exists to mirror exactly what clampHRWaitBudget enforces at runtime (see
// phases.go), and that function subtracts both, so a static check that only knew about
// one of them could pass while the runtime clamp still floors the wait on every run.
//
// This is the test that is meant to fail the moment the two files drift apart again:
// either a wait-budget constant grows without the matching argo deadline, or a deadline
// shrinks without the matching wait-budget constant. Both directions broke the invariant
// before this fix (provision's 90m wait inside a 90m deadline).
func TestDeadlineInvariant(t *testing.T) {
	deadlines := scenarioStepDeadlines(t, scenarioWorkflowPath)

	// step name (10-scenario.yaml) -> the Go wait-budget constant that step's
	// validateStack call passes to hrWaitBudget (phases.go: Provision uses
	// hrWaitBudgetInstall, Validate uses hrWaitBudgetUpgrade). "upgrade" is
	// deliberately absent: Upgrade applies the target ref and returns without ever
	// calling validateStack, so it has no wait budget for this invariant to check.
	waitSteps := map[string]time.Duration{
		"provision": hrWaitBudgetInstall,
		"validate":  hrWaitBudgetUpgrade,
	}
	reserve := hrWaitReserve + hrPodStartupSlack

	for stepName, budget := range waitSteps {
		secs, ok := deadlines[stepName]
		if !ok {
			t.Fatalf("%s: step %q has no numeric \"deadline\" argument in the scenario template — expected one for a step that runs the HelmRelease wait", scenarioWorkflowPath, stepName)
		}
		deadline := time.Duration(secs) * time.Second
		required := budget + reserve
		if required > deadline {
			t.Errorf("%s step %q: wait budget %s + reserve %s (dump+upload+margin) + %s (pod-start slack) = %s exceeds its pod deadline %s (%ds) — the pod will be killed before the HelmRelease wait can expire and trigger the failure-time dump/upload. Raise the step's deadline in %s or lower the wait budget in phases.go.",
				scenarioWorkflowPath, stepName, formatMinutes(budget), formatMinutes(hrWaitReserve), formatMinutes(hrPodStartupSlack), formatMinutes(required), formatMinutes(deadline), secs, scenarioWorkflowPath)
		}
	}
}
