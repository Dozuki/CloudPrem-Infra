package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The notify script (argo/50-janitor-cron.yaml, template post-report) is the only thing
// standing between a report and a human, and it is bash+jq that nothing else executes
// until 09:00 ET in production. Every previous bug in it - a report tag pasted into
// single quotes and detonating on an apostrophe, a selector that rendered clean
// destroys under a "teardown FAILED" headline, states that matched nothing at all -
// would have been caught by running the thing once against a realistic report.
//
// So these tests do exactly that: pull the script out of the YAML it ships in, run it
// under a real bash with a real jq, and read what it tried to post. `vault` and `curl`
// are stubbed on PATH (the script's only two external side effects), so the assertions
// are on the actual Slack payload.

// notifyScript extracts the post-report template's inline script from the cron YAML.
// Parsing the YAML rather than string-slicing it means the test breaks loudly if the
// template is restructured, instead of silently testing nothing.
func notifyScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../argo/50-janitor-cron.yaml")
	if err != nil {
		t.Fatalf("read 50-janitor-cron.yaml: %v", err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	for {
		var doc struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Templates []struct {
					Name      string `yaml:"name"`
					Container struct {
						Args []string `yaml:"args"`
					} `yaml:"container"`
				} `yaml:"templates"`
			} `yaml:"spec"`
		}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		for _, tpl := range doc.Spec.Templates {
			if tpl.Name == "post-report" && len(tpl.Container.Args) > 0 {
				return tpl.Container.Args[0]
			}
		}
	}
	t.Fatal("no post-report template with a container script found in 50-janitor-cron.yaml")
	return ""
}

func argoManifest(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("../argo", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

type janitorManifestDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		Suspend                 bool     `yaml:"suspend"`
		Schedules               []string `yaml:"schedules"`
		ConcurrencyPolicy       string   `yaml:"concurrencyPolicy"`
		StartingDeadlineSeconds int      `yaml:"startingDeadlineSeconds"`
		WorkflowSpec            struct {
			Entrypoint            string `yaml:"entrypoint"`
			ActiveDeadlineSeconds int    `yaml:"activeDeadlineSeconds"`
		} `yaml:"workflowSpec"`
		Volumes []struct {
			Name     string `yaml:"name"`
			EmptyDir struct {
				SizeLimit string `yaml:"sizeLimit"`
			} `yaml:"emptyDir"`
		} `yaml:"volumes"`
		Templates []struct {
			Name                  string `yaml:"name"`
			ActiveDeadlineSeconds int    `yaml:"activeDeadlineSeconds"`
			Metadata              struct {
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"metadata"`
			Container struct {
				Args      []string `yaml:"args"`
				Resources struct {
					Requests map[string]string `yaml:"requests"`
					Limits   map[string]string `yaml:"limits"`
				} `yaml:"resources"`
			} `yaml:"container"`
		} `yaml:"templates"`
	} `yaml:"spec"`
}

func janitorManifestDocs(t *testing.T) []janitorManifestDoc {
	t.Helper()
	manifest := argoManifest(t, "50-janitor-cron.yaml")
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	var docs []janitorManifestDoc
	for {
		var doc janitorManifestDoc
		if err := dec.Decode(&doc); err != nil {
			if err != io.EOF {
				t.Fatalf("decode 50-janitor-cron.yaml: %v", err)
			}
			break
		}
		docs = append(docs, doc)
	}
	return docs
}

func TestResourceReaperJanitorYAMLContract(t *testing.T) {
	config := argoManifest(t, "00-phase-templates.yaml")
	janitor := argoManifest(t, "50-janitor-cron.yaml")

	for _, key := range []string{
		"reaper_report_bucket:",
		"reaper_action_queue_url:",
		"reaper_result_queue_url:",
		"reaper_control_table:",
		"reaper_shadow: 'true'",
		"reaper_actions_enabled: 'false'",
		"reaper_direct_slack_enabled: 'false'",
	} {
		if !strings.Contains(config, key) {
			t.Errorf("harness ConfigMap is missing explicit Resource Reaper setting %q", key)
		}
	}
	if strings.Contains(config, "janitor_mode:") {
		t.Fatal("obsolete janitor_mode ConfigMap key remains")
	}
	for _, forbidden := range []string{"- name: mode", "-p mode=sweep", "--sweep=true"} {
		if strings.Contains(janitor, forbidden) {
			t.Errorf("legacy direct sweep entrypoint remains: %q", forbidden)
		}
	}
	for _, required := range []string{
		"reaper-direct-slack-enabled",
	} {
		if !strings.Contains(janitor, required) {
			t.Errorf("daily report contract is missing %q", required)
		}
	}
	if !strings.Contains(janitor, "when: \"'{{workflow.parameters.reaper-direct-slack-enabled}}' == 'true'\"") {
		t.Fatal("legacy direct Slack comparison is not explicitly guarded for removal at cutover")
	}
}

func TestResourceReaperActionWorkerYAMLContract(t *testing.T) {
	manifest := argoManifest(t, "50-janitor-cron.yaml")
	checkIndex := strings.Index(manifest, `if [ "${REAPER_ACTIONS_ENABLED}" != true ]`)
	receiveIndex := strings.Index(manifest, "/usr/local/bin/docker-entrypoint.sh reaper-worker")
	if checkIndex < 0 || receiveIndex < 0 || checkIndex > receiveIndex {
		t.Fatal("actions-disabled gate must appear before the worker can receive a message")
	}
	if !strings.Contains(manifest, "VisibilityTimeoutSeconds: 13500") && !strings.Contains(workerCLISource(t), "VisibilityTimeoutSeconds: 13500") {
		t.Fatal("worker and manifest no longer document the 13,500-second queue visibility contract")
	}
	if strings.Contains(manifest, "volumeClaimTemplates:") {
		t.Fatal("five-minute empty queue checks must not provision persistent volumes")
	}

	var workflowFound, scanFound, queueCheckFound, executeFound, dailyFound, workerFound bool
	for _, doc := range janitorManifestDocs(t) {
		switch {
		case doc.Kind == "WorkflowTemplate" && doc.Metadata.Name == "harness-janitor":
			workflowFound = true
			if len(doc.Spec.Volumes) != 1 || doc.Spec.Volumes[0].Name != "workspace" || doc.Spec.Volumes[0].EmptyDir.SizeLimit != "10Gi" {
				t.Fatalf("harness workspace must be one size-limited 10Gi emptyDir: %#v", doc.Spec.Volumes)
			}
			for _, tpl := range doc.Spec.Templates {
				switch tpl.Name {
				case "scan":
					scanFound = true
					if tpl.ActiveDeadlineSeconds != 1800 {
						t.Errorf("scan activeDeadlineSeconds = %d, want 1800", tpl.ActiveDeadlineSeconds)
					}
					script := strings.Join(tpl.Container.Args, "\n")
					for _, flag := range []string{"--sweep=false", "--reaper-report-bucket"} {
						if !strings.Contains(script, flag) {
							t.Errorf("scan command is missing %q", flag)
						}
					}
					assertResources(t, "scan", tpl.Container.Resources.Requests, tpl.Container.Resources.Limits,
						map[string]string{"cpu": "1", "memory": "4Gi", "ephemeral-storage": "10Gi"},
						map[string]string{"memory": "6Gi", "ephemeral-storage": "12Gi"})
				case "queue-check":
					queueCheckFound = true
					if tpl.ActiveDeadlineSeconds != 120 {
						t.Errorf("queue-check activeDeadlineSeconds = %d, want 120", tpl.ActiveDeadlineSeconds)
					}
					assertResources(t, "queue-check", tpl.Container.Resources.Requests, tpl.Container.Resources.Limits,
						map[string]string{"cpu": "100m", "memory": "256Mi"},
						map[string]string{"memory": "256Mi"})
				case "execute":
					executeFound = true
					if tpl.ActiveDeadlineSeconds != 12600 {
						t.Errorf("execute activeDeadlineSeconds = %d, want 12600", tpl.ActiveDeadlineSeconds)
					}
					if got := tpl.Metadata.Annotations["karpenter.sh/do-not-disrupt"]; got != "true" {
						t.Errorf("execute do-not-disrupt annotation = %q, want true", got)
					}
					script := strings.Join(tpl.Container.Args, "\n")
					for _, flag := range []string{"reaper-worker", "--action-queue-url", "--result-queue-url", "--control-table"} {
						if !strings.Contains(script, flag) {
							t.Errorf("execute command is missing %q", flag)
						}
					}
					assertResources(t, "execute", tpl.Container.Resources.Requests, tpl.Container.Resources.Limits,
						map[string]string{"cpu": "1", "memory": "4Gi", "ephemeral-storage": "10Gi"},
						map[string]string{"memory": "6Gi", "ephemeral-storage": "12Gi"})
				default:
					if tpl.Container.Resources.Requests["ephemeral-storage"] != "" || tpl.Container.Resources.Limits["ephemeral-storage"] != "" {
						t.Errorf("template %q unexpectedly reserves workspace ephemeral storage", tpl.Name)
					}
				}
			}
		case doc.Kind == "CronWorkflow" && doc.Metadata.Name == "harness-janitor":
			dailyFound = true
			if doc.Spec.Suspend {
				t.Error("daily report CronWorkflow must remain active in shadow mode")
			}
			if doc.Spec.StartingDeadlineSeconds != 600 || doc.Spec.WorkflowSpec.ActiveDeadlineSeconds != 2400 {
				t.Errorf("daily deadlines = start %d workflow %d, want 600 and 2400", doc.Spec.StartingDeadlineSeconds, doc.Spec.WorkflowSpec.ActiveDeadlineSeconds)
			}
			if doc.Spec.ConcurrencyPolicy != "Forbid" {
				t.Errorf("daily concurrencyPolicy = %q, want Forbid", doc.Spec.ConcurrencyPolicy)
			}
		case doc.Kind == "CronWorkflow" && doc.Metadata.Name == "harness-reaper-worker":
			workerFound = true
			if !doc.Spec.Suspend {
				t.Error("action worker CronWorkflow must remain suspended until cutover")
			}
			if doc.Spec.WorkflowSpec.Entrypoint != "consume-one" || doc.Spec.WorkflowSpec.ActiveDeadlineSeconds != 14400 {
				t.Errorf("worker workflow = entrypoint %q deadline %d, want consume-one and 14400", doc.Spec.WorkflowSpec.Entrypoint, doc.Spec.WorkflowSpec.ActiveDeadlineSeconds)
			}
			if doc.Spec.ConcurrencyPolicy != "Forbid" || len(doc.Spec.Schedules) != 1 || doc.Spec.Schedules[0] != "*/5 * * * *" {
				t.Errorf("worker schedule/concurrency = %#v/%q, want five-minute Forbid", doc.Spec.Schedules, doc.Spec.ConcurrencyPolicy)
			}
		}
	}
	if !workflowFound || !scanFound || !queueCheckFound || !executeFound || !dailyFound || !workerFound {
		t.Fatalf("missing expected janitor documents: workflow=%t scan=%t queue-check=%t execute=%t daily=%t worker=%t", workflowFound, scanFound, queueCheckFound, executeFound, dailyFound, workerFound)
	}
}

func assertResources(t *testing.T, template string, gotRequests, gotLimits, wantRequests, wantLimits map[string]string) {
	t.Helper()
	if !sameStringMap(gotRequests, wantRequests) {
		t.Errorf("%s requests = %#v, want %#v", template, gotRequests, wantRequests)
	}
	if !sameStringMap(gotLimits, wantLimits) {
		t.Errorf("%s limits = %#v, want %#v", template, gotLimits, wantLimits)
	}
}

func sameStringMap(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func workerCLISource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../cmd/harness/reaper_worker.go")
	if err != nil {
		t.Fatalf("read reaper_worker.go: %v", err)
	}
	return string(body)
}

// runNotify executes the script with REPORT/SCAN_STATUS/MODE set, against stub vault
// and curl binaries, and returns (posted slack text, "" if nothing was posted, stdout).
func runNotify(t *testing.T, report, scanStatus, mode string) (posted, stdout string) {
	t.Helper()
	for _, bin := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available; the notify script cannot be exercised here", bin)
		}
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "notify.sh")
	if err := os.WriteFile(scriptPath, []byte(notifyScript(t)), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	// Stub PATH entries for the script's only two side effects. vault answers any
	// subcommand with a token; curl writes the payload it was handed to a file.
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	postFile := filepath.Join(dir, "posted.json")
	stubs := map[string]string{
		"vault": "#!/bin/sh\necho stub-token\nexit 0\n",
		"curl":  "#!/bin/sh\ncat > " + postFile + "\nexit 0\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o700); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"REPORT="+report,
		"SCAN_STATUS="+scanStatus,
		"MODE="+mode,
		"SLACK_CHANNEL=#harness-test",
		"VAULT_ADDR=http://vault.invalid:8200",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("notify script exited non-zero (%v); it carries continueOn: failed and must always exit 0\n%s", err, out)
	}
	body, rerr := os.ReadFile(postFile)
	if rerr != nil {
		return "", string(out) // nothing posted
	}
	var payload struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
	}
	if jerr := json.Unmarshal(body, &payload); jerr != nil {
		t.Fatalf("the payload handed to curl is not JSON (%v): %s", jerr, body)
	}
	if payload.Channel != "#harness-test" {
		t.Fatalf("posted to channel %q, want the SLACK_CHANNEL value", payload.Channel)
	}
	return payload.Text, string(out)
}

// sampleSweepReport is a realistic post-sweep report: one candidate destroyed cleanly
// (State stays orphan on purpose, only sweep_result changes), one residue, one unknown
// (destroy ran, verification failed), one needs-review, and one clean. The reason text
// carries the apostrophes classify() really emits ("the janitor's --region"), which is
// what used to detonate the script at parse time.
func sampleSweepReport(t *testing.T) string {
	t.Helper()
	rep := Report{
		SchemaVersion: JanitorReportSchemaVersion,
		Mode:          "sweep",
		At:            time.Now().UTC().Format(time.RFC3339),
		Account:       testAccount,
		Candidates: []Candidate{
			{Prefix: "run1-min_default/", RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min",
				DeleteAfter: "2026-08-04T00:00:00Z", State: StateOrphan, Resources: 12,
				Reason: "12 resources still live past delete_after + grace with no owner: a teardown FAILED", SweepResult: "destroyed"},
			{Prefix: "run2-bi/", RunID: "run2", ConfigName: "bi_default", Identifier: "smokebb-bi",
				DeleteAfter: "2026-08-03T00:00:00Z", State: StateResidue, Resources: 3,
				Reason:      "terraform destroy completed, but 3 tagged resources survived; it is state, not the janitor's reach, that is the problem",
				SweepResult: "residue: needs manual cleanup"},
			{Prefix: "run3-min_default/", RunID: "run3", ConfigName: "min_default", Identifier: "smokecc-min",
				DeleteAfter: "2026-08-03T00:00:00Z", State: StateUnknown, Resources: 5,
				Reason:      "the run's recorded identity, not today's",
				SweepResult: "destroy ran, but the post-destroy verification query failed: tagging api unavailable"},
			{Prefix: "run4-recover/", RunID: "run4", ConfigName: "recover", Identifier: "smokerec-min",
				State: StateNeedsReview, Reason: "recovery drill config, never sweepable"},
			{Prefix: "run5-min_default/", RunID: "run5", ConfigName: "min_default", Identifier: "smokedd-min",
				DeleteAfter: "2026-08-01T00:00:00Z", State: StateClean, Reason: "stale and unowned, no tagged resources remain"},
		},
		Orphans: 0, Swept: 1, Failed: 0, Residue: 1, Inconclusive: 1,
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal sample report: %v", err)
	}
	return string(b)
}

// TestNotifyScriptSelectsTheRightCandidates is the regression test for the selector
// bug: a clean destroy must not be counted as something needing a human, and unknown
// and needs-review must not be invisible.
func TestNotifyScriptSelectsTheRightCandidates(t *testing.T) {
	text, out := runNotify(t, sampleSweepReport(t), "Succeeded", "sweep")
	if text == "" {
		t.Fatalf("nothing was posted for a report with residue, unknown and needs-review candidates\n%s", out)
	}

	// Counts: residue + unknown need a human; needs-review is its own bucket; the
	// destroyed candidate is informational and must NOT inflate the alarm count even
	// though its state is still "orphan".
	if !strings.Contains(text, "2 test stack(s) need a human") {
		t.Errorf("headline does not count residue+unknown as the 2 needing a human:\n%s", text)
	}
	if !strings.Contains(text, "1 need review") {
		t.Errorf("headline does not count the needs-review candidate:\n%s", text)
	}
	if !strings.Contains(text, "1 destroyed this cycle") {
		t.Errorf("headline does not report the destroyed candidate:\n%s", text)
	}

	// Every candidate that matters is named, and the clean one is not.
	for _, want := range []string{"run2", "run3", "run4", "run1"} {
		if !strings.Contains(text, "`"+want+"`") {
			t.Errorf("candidate %s is missing from the message:\n%s", want, text)
		}
	}
	if strings.Contains(text, "`run5`") {
		t.Error("the clean candidate was reported; clean is neither alerted nor swept")
	}

	// The destroyed candidate belongs under the informational heading, not the alarm
	// one - the whole point of excluding sweep_result "destroyed" from the alarm set.
	alarmIdx := strings.Index(text, "NEEDS A HUMAN")
	if alarmIdx < 0 {
		t.Fatalf("alarm heading missing from the rendered card:\n%s", text)
	}
	alarmSection := text[alarmIdx:]
	if idx := strings.Index(alarmSection, "destroyed this cycle"); idx >= 0 {
		if strings.Contains(alarmSection[:idx], "`run1`") {
			t.Errorf("the destroyed candidate is listed under NEEDS A HUMAN:\n%s", text)
		}
	}

	// Apostrophes in the reason/sweep_result survive the whole pipeline.
	if !strings.Contains(text, "verification query failed") {
		t.Errorf("the unknown candidate's sweep_result did not survive the pipeline:\n%s", text)
	}
}

// TestNotifyScriptStaysQuietWhenThereIsNothingToSay: a report whose only candidates are
// active/clean posts nothing at all. A janitor that pages every night for a healthy
// account gets muted, and then it pages for nothing when it matters.
func TestNotifyScriptStaysQuietWhenThereIsNothingToSay(t *testing.T) {
	rep := Report{
		SchemaVersion: JanitorReportSchemaVersion, Mode: "report", At: "2026-08-05T09:00:00Z",
		Candidates: []Candidate{
			{Prefix: "run1-min_default/", RunID: "run1", ConfigName: "min_default", State: StateClean, Reason: "no tagged resources remain"},
			{Prefix: "run2-min_default/", RunID: "run2", ConfigName: "min_default", State: StateActive, Reason: "owned by a live workflow (phase Running)"},
		},
	}
	b, _ := json.Marshal(rep)
	text, out := runNotify(t, string(b), "Succeeded", "report")
	if text != "" {
		t.Fatalf("posted %q for a report with nothing to say", text)
	}
	if !strings.Contains(out, "nothing to report") {
		t.Fatalf("stdout does not say why it stayed quiet:\n%s", out)
	}
}

// TestNotifyScriptReportsASafetyAbort: a G1-G3 abort returns before Scan runs, so
// REPORT is the outputs default '{}' and SCAN_STATUS is not Succeeded. That must post
// the "scan failed" message rather than nothing.
func TestNotifyScriptReportsASafetyAbort(t *testing.T) {
	text, out := runNotify(t, "{}", "Failed", "report")
	if text == "" {
		t.Fatalf("nothing posted for a safety abort\n%s", out)
	}
	if !strings.Contains(text, "the SCAN step failed") {
		t.Errorf("message does not name the scan failure:\n%s", text)
	}
}

// TestNotifyScriptRefusesAnUnknownSchema: the schema assert must become the Slack
// message. Exiting non-zero instead would post nothing and fail nothing (continueOn:
// failed) - a silent blackout, which is the exact thing the assert exists to prevent.
func TestNotifyScriptRefusesAnUnknownSchema(t *testing.T) {
	text, out := runNotify(t, `{"schema_version":99,"candidates":[{"state":"orphan","run_id":"run1"}]}`, "Succeeded", "sweep")
	if text == "" {
		t.Fatalf("nothing posted on a schema mismatch\n%s", out)
	}
	if !strings.Contains(text, "CANNOT READ THIS CYCLE'S REPORT") {
		t.Errorf("message does not say the report is unreadable:\n%s", text)
	}
	if strings.Contains(text, "`run1`") {
		t.Error("the script read candidate fields after declaring the schema untrustworthy")
	}
}

// TestNotifyScriptSurvivesAMidSweepAbortReport: the abort path now prints its partial
// report as the last stdout line (cmd/harness main.go), so the script gets a real
// report AND a non-Succeeded status. It has to say both things.
func TestNotifyScriptSurvivesAMidSweepAbortReport(t *testing.T) {
	rep := Report{
		SchemaVersion: JanitorReportSchemaVersion, Mode: "sweep", At: "2026-08-05T09:00:00Z",
		Candidates: []Candidate{
			{Prefix: "run1-min_default/", RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min",
				DeleteAfter: "2026-08-04T00:00:00Z", State: StateOrphan, Resources: 9,
				Reason: "a teardown FAILED", SweepResult: "failed: destroy: exit status 1"},
		},
		Orphans: 1, Failed: 1,
	}
	b, _ := json.Marshal(rep)
	text, out := runNotify(t, string(b), "Failed", "sweep")
	if text == "" {
		t.Fatalf("nothing posted for a failed sweep that left a report\n%s", out)
	}
	if !strings.Contains(text, "did not finish cleanly") {
		t.Errorf("message does not name the failed step:\n%s", text)
	}
	if !strings.Contains(text, "`run1`") || !strings.Contains(text, "failed: destroy") {
		t.Errorf("message does not carry the candidate that failed:\n%s", text)
	}
}

// TestNotifyScriptCapsLongSections pins the SECTION_CAP behavior added after the
// first real cycle (46 pre-fix needs-review candidates) split the Slack post into a
// multi-message wall: sections render at most 10 lines plus an exact "...and N more"
// trailer, while the headline still carries the true total.
func TestNotifyScriptCapsLongSections(t *testing.T) {
	rep := Report{
		SchemaVersion: JanitorReportSchemaVersion,
		Mode:          "report",
		At:            time.Now().UTC().Format(time.RFC3339),
		Account:       testAccount,
	}
	for i := 0; i < 15; i++ {
		rep.Candidates = append(rep.Candidates, Candidate{
			Prefix: fmt.Sprintf("capred%02d-min_default/", i), RunID: fmt.Sprintf("capred%02d", i),
			ConfigName: "min_default", Identifier: fmt.Sprintf("smokecap%02d-min", i),
			DeleteAfter: "2026-08-01T00:00:00Z", State: StateNeedsReview,
			Reason: "manifest predates applied-customer recording",
		})
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal cap report: %v", err)
	}
	text, out := runNotify(t, string(b), "Succeeded", "report")
	if text == "" {
		t.Fatalf("nothing was posted for a 15-candidate needs-review report\n%s", out)
	}
	if !strings.Contains(text, "15 need review") {
		t.Errorf("headline does not carry the true total of 15:\n%s", text)
	}
	if got := strings.Count(text, "- `capred"); got != 10 {
		t.Errorf("rendered %d candidate lines, want the SECTION_CAP of 10:\n%s", got, text)
	}
	if !strings.Contains(text, "- ...and 5 more; full list in the scan pod log and the JSON report output") {
		t.Errorf("cap trailer missing or wrong:\n%s", text)
	}
}

// TestNotifyScriptCapBoundaries pins the exact cap edges and per-section
// independence: 10 candidates render with no trailer, 11 render ten lines plus an
// "...and 1 more" trailer with the eleventh ID absent, and a long alarm section is
// capped without touching a short review section in the same message.
func TestNotifyScriptCapBoundaries(t *testing.T) {
	mk := func(prefix string, n int, state CandidateState, resources int) []Candidate {
		var cs []Candidate
		for i := 0; i < n; i++ {
			cs = append(cs, Candidate{
				Prefix: fmt.Sprintf("%s%02d-min_default/", prefix, i), RunID: fmt.Sprintf("%s%02d", prefix, i),
				ConfigName: "min_default", Identifier: fmt.Sprintf("smoke%s%02d-min", prefix, i),
				DeleteAfter: "2026-08-01T00:00:00Z", State: state, Resources: resources,
				Reason: "boundary fixture",
			})
		}
		return cs
	}
	run := func(cs []Candidate) string {
		rep := Report{
			SchemaVersion: JanitorReportSchemaVersion, Mode: "report",
			At: time.Now().UTC().Format(time.RFC3339), Account: testAccount, Candidates: cs,
		}
		b, err := json.Marshal(rep)
		if err != nil {
			t.Fatalf("marshal boundary report: %v", err)
		}
		text, out := runNotify(t, string(b), "Succeeded", "report")
		if text == "" {
			t.Fatalf("nothing posted for a boundary report\n%s", out)
		}
		return text
	}

	// Exactly 10: every line renders, no trailer.
	text := run(mk("ten", 10, StateNeedsReview, 0))
	if got := strings.Count(text, "- `ten"); got != 10 {
		t.Errorf("10-candidate section rendered %d lines, want all 10:\n%s", got, text)
	}
	if strings.Contains(text, "...and") {
		t.Errorf("10-candidate section must not carry a trailer:\n%s", text)
	}

	// Exactly 11: ten lines, "...and 1 more", true headline, eleventh ID absent.
	text = run(mk("elv", 11, StateNeedsReview, 0))
	if got := strings.Count(text, "- `elv"); got != 10 {
		t.Errorf("11-candidate section rendered %d lines, want 10:\n%s", got, text)
	}
	if !strings.Contains(text, "- ...and 1 more; full list in the scan pod log and the JSON report output") {
		t.Errorf("11-candidate trailer missing or wrong:\n%s", text)
	}
	if !strings.Contains(text, "11 need review") {
		t.Errorf("headline must carry the true total of 11:\n%s", text)
	}
	if strings.Contains(text, "`elv10`") {
		t.Errorf("the eleventh candidate leaked past the cap:\n%s", text)
	}

	// A destroyed section past the cap: same renderer, but pin it anyway so the
	// least-watched section cannot drift away from the others.
	done := mk("done", 12, StateOrphan, 0)
	for i := range done {
		done[i].SweepResult = "destroyed"
	}
	text = run(done)
	if got := strings.Count(text, "- `done"); got != 10 {
		t.Errorf("destroyed section rendered %d lines, want 10:\n%s", got, text)
	}
	if !strings.Contains(text, "- ...and 2 more; full list in the scan pod log and the JSON report output") {
		t.Errorf("destroyed trailer missing or wrong:\n%s", text)
	}
	if !strings.Contains(text, "12 destroyed this cycle") {
		t.Errorf("headline must carry the true destroyed total of 12:\n%s", text)
	}

	// Mixed sections: 12 orphans cap at 10 while all 3 review lines survive.
	mixed := append(mk("alarm", 12, StateOrphan, 1), mk("rev", 3, StateNeedsReview, 0)...)
	text = run(mixed)
	if got := strings.Count(text, "- `alarm"); got != 10 {
		t.Errorf("alarm section rendered %d lines, want 10:\n%s", got, text)
	}
	if !strings.Contains(text, "- ...and 2 more; full list in the scan pod log and the JSON report output") {
		t.Errorf("alarm trailer missing or wrong:\n%s", text)
	}
	if got := strings.Count(text, "- `rev"); got != 3 {
		t.Errorf("review section rendered %d lines, want all 3 (cap must be per-section):\n%s", got, text)
	}
	if !strings.Contains(text, "12 test stack(s) need a human") || !strings.Contains(text, "3 need review") {
		t.Errorf("mixed headline totals wrong:\n%s", text)
	}
}

// TestNotifyScriptFlattensEmbeddedNewlines pins the LINE_DEF gsub: sweep_result
// carries raw err.Error() text on failed destroys and terraform errors span lines,
// so the section cap is a physical-line cap only if the renderer flattens embedded
// newlines instead of letting one candidate render as several lines.
func TestNotifyScriptFlattensEmbeddedNewlines(t *testing.T) {
	rep := Report{
		SchemaVersion: JanitorReportSchemaVersion,
		Mode:          "report",
		At:            time.Now().UTC().Format(time.RFC3339),
		Account:       testAccount,
		Candidates: []Candidate{{
			Prefix: "nl00-min_default/", RunID: "nl00",
			ConfigName: "min_default", Identifier: "smokenl00-min",
			DeleteAfter: "2026-08-01T00:00:00Z", State: StateOrphan, Resources: 2,
			SweepResult: "failed: exit status 1\nError: deleting subnet\nstill has dependencies",
		}},
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal newline report: %v", err)
	}
	text, out := runNotify(t, string(b), "Succeeded", "report")
	if text == "" {
		t.Fatalf("nothing posted for the newline report\n%s", out)
	}
	if strings.Contains(text, "exit status 1\nError") {
		t.Errorf("embedded newline survived into the rendered line:\n%s", text)
	}
	if !strings.Contains(text, "failed: exit status 1 Error: deleting subnet still has dependencies") {
		t.Errorf("flattened sweep_result missing or wrong:\n%s", text)
	}
	if got := strings.Count(text, "- `nl00"); got != 1 {
		t.Errorf("candidate rendered %d marker lines, want 1:\n%s", got, text)
	}
}
