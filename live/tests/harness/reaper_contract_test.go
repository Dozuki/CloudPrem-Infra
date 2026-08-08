package harness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const contractAccount = "076248559428"

func contractCandidate(state CandidateState) Candidate {
	return Candidate{
		Bucket:        "state-bucket",
		Prefix:        "/runs/bi-ha/",
		RunID:         "run-123",
		ConfigName:    "bi_ha",
		DeleteAfter:   "2026-08-07T08:00:00Z",
		State:         state,
		Reason:        "candidate reason",
		Resources:     12,
		WorkflowPhase: "",
		Region:        "us-west-2",
		DRRegion:      "us-east-1",
	}
}

func contractReport(candidate Candidate) Report {
	return Report{
		SchemaVersion: JanitorReportSchemaVersion,
		Mode:          "report",
		At:            "2026-08-08T13:00:00Z",
		Account:       contractAccount,
		Candidates:    []Candidate{candidate},
	}
}

func TestToEngineReportMapsOrphanAsOneEligibleStack(t *testing.T) {
	got, err := ToEngineReport(contractReport(contractCandidate(StateOrphan)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CleanupUnits) != 1 || got.CleanupUnits[0].Decision != "eligible" {
		t.Fatalf("%+v", got)
	}
	if got.CleanupUnits[0].Evidence.ResourceCount != 12 {
		t.Fatalf("%+v", got.CleanupUnits[0])
	}
	if !reflect.DeepEqual(got.CleanupUnits[0].Capabilities, []string{"explain", "hold", "sweep"}) {
		t.Fatalf("capabilities = %#v", got.CleanupUnits[0].Capabilities)
	}
}

func TestToEngineReportMapsEveryCandidateState(t *testing.T) {
	cases := []struct {
		state    CandidateState
		decision string
	}{
		{StateActive, "protected"},
		{StatePending, "held"},
		{StateClean, "already_gone"},
		{StateOrphan, "eligible"},
		{StateKept, "protected"},
		{StateBlocked, "needs_review"},
		{StateNeedsReview, "needs_review"},
		{StateUnknown, "needs_review"},
		{StateResidue, "needs_review"},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			got, err := ToEngineReport(contractReport(contractCandidate(tc.state)))
			if err != nil {
				t.Fatal(err)
			}
			unit := got.CleanupUnits[0]
			if unit.Decision != tc.decision {
				t.Fatalf("decision = %q, want %q", unit.Decision, tc.decision)
			}
			if tc.state != StateOrphan && containsString(unit.Capabilities, "sweep") {
				t.Fatalf("non-orphan capabilities = %#v", unit.Capabilities)
			}
			if (tc.state == StateUnknown || tc.state == StateResidue) && got.Status != "degraded" {
				t.Fatalf("status = %q, want degraded", got.Status)
			}
		})
	}
}

func TestDestroyedSweepResultTakesPrecedenceOverRetainedOrphanState(t *testing.T) {
	candidate := contractCandidate(StateOrphan)
	candidate.SweepResult = sweepResultDestroyed
	got, err := ToEngineReport(contractReport(candidate))
	if err != nil {
		t.Fatal(err)
	}
	unit := got.CleanupUnits[0]
	if unit.Decision != "already_gone" || containsString(unit.Capabilities, "sweep") {
		t.Fatalf("%+v", unit)
	}
}

func TestResidueCarriesExactIndexLagCaveat(t *testing.T) {
	got, err := ToEngineReport(contractReport(contractCandidate(StateResidue)))
	if err != nil {
		t.Fatal(err)
	}
	if got.CleanupUnits[0].Evidence.ResidueIndexLagCaveat != residueIndexLagCaveat {
		t.Fatalf("caveat = %q", got.CleanupUnits[0].Evidence.ResidueIndexLagCaveat)
	}
}

func TestFindingVersionChangesWhenRunReusesPrefix(t *testing.T) {
	a := contractCandidate(StateOrphan)
	b := a
	b.RunID = "run-456"
	if FindingVersion(a, "eligible") == FindingVersion(b, "eligible") {
		t.Fatal("run id must version a finding")
	}
	if FindingID("harness", contractAccount, CanonicalHarnessIdentity(a.Bucket, a.Prefix)) !=
		FindingID("harness", contractAccount, CanonicalHarnessIdentity(b.Bucket, b.Prefix)) {
		t.Fatal("run id must not change the stable finding id")
	}
}

func TestFindingVersionCoversCleanupRelevantFields(t *testing.T) {
	base := contractCandidate(StateOrphan)
	baseVersion := FindingVersion(base, "eligible")
	cases := []struct {
		name     string
		decision string
		mutate   func(*Candidate)
	}{
		{"decision", "protected", func(*Candidate) {}},
		{"expiry", "eligible", func(c *Candidate) { c.DeleteAfter = "2026-08-09T08:00:00Z" }},
		{"resource count", "eligible", func(c *Candidate) { c.Resources++ }},
		{"workflow phase", "eligible", func(c *Candidate) { c.WorkflowPhase = "Running" }},
		{"keep on failure", "eligible", func(c *Candidate) { c.KeepOnFailure = true }},
		{"lock present", "eligible", func(c *Candidate) { c.LockAge = "5h0m0s" }},
		{"regions", "eligible", func(c *Candidate) { c.DRRegion = "us-gov-west-1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			tc.mutate(&candidate)
			if got := FindingVersion(candidate, tc.decision); got == baseVersion {
				t.Fatalf("version did not change: %s", got)
			}
		})
	}

	cosmetic := base
	cosmetic.Reason = "different human text"
	if got := FindingVersion(cosmetic, "eligible"); got != baseVersion {
		t.Fatalf("reason changed version: %s != %s", got, baseVersion)
	}
	cosmetic.LockAge = "9h0m0s"
	base.LockAge = "5h0m0s"
	if FindingVersion(cosmetic, "eligible") != FindingVersion(base, "eligible") {
		t.Fatal("lock age changed version even though lock presence stayed true")
	}
}

func TestGoIdentityAndHashesMatchPythonGoldenVector(t *testing.T) {
	body, err := os.ReadFile("testdata/reaper-contract-v1-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		CanonicalIdentity string   `json:"canonical_identity"`
		FindingID         string   `json:"finding_id"`
		Version           string   `json:"version"`
		Regions           []string `json:"regions"`
	}
	if err := json.Unmarshal(body, &golden); err != nil {
		t.Fatal(err)
	}
	candidate := contractCandidate(StateOrphan)
	candidate.Region, candidate.DRRegion = golden.Regions[0], golden.Regions[1]
	for _, prefix := range []string{"/runs/bi-ha/", "runs/bi-ha"} {
		candidate.Prefix = prefix
		identity := CanonicalHarnessIdentity(candidate.Bucket, candidate.Prefix)
		if identity != golden.CanonicalIdentity {
			t.Fatalf("identity = %q, want %q", identity, golden.CanonicalIdentity)
		}
		if got := FindingID("harness", contractAccount, identity); got != golden.FindingID {
			t.Fatalf("finding id = %q, want %q", got, golden.FindingID)
		}
		if got := FindingVersion(candidate, "eligible"); got != golden.Version {
			t.Fatalf("version = %q, want %q", got, golden.Version)
		}
	}
}

func TestToEngineReportRejectsInvalidSourceValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Report)
		want   string
	}{
		{"invalid observed timestamp", func(r *Report) { r.At = "not-a-time" }, "observed_at"},
		{"invalid expiry timestamp", func(r *Report) { r.Candidates[0].DeleteAfter = "tomorrow" }, "expires_at"},
		{"nul bucket", func(r *Report) { r.Candidates[0].Bucket = "state\x00bucket" }, "NUL"},
		{"nul run id", func(r *Report) { r.Candidates[0].RunID = "run\x00123" }, "NUL"},
		{"empty prefix", func(r *Report) { r.Candidates[0].Prefix = "///" }, "state_prefix"},
		{"missing region", func(r *Report) { r.Candidates[0].Region = ""; r.Candidates[0].DRRegion = "" }, "region"},
		{"unknown state", func(r *Report) { r.Candidates[0].State = CandidateState("future") }, "state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := contractReport(contractCandidate(StateOrphan))
			tc.mutate(&rep)
			if _, err := ToEngineReport(rep); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestToEngineReportPreservesEarlyNeedsReviewCandidate(t *testing.T) {
	candidate := Candidate{
		Bucket: "prefix-dozuki-terraform-state-us-east-1-" + contractAccount,
		Prefix: "unreadable-run/",
		State:  StateNeedsReview,
		Reason: "manifest unreadable",
	}
	got, err := ToEngineReport(contractReport(candidate))
	if err != nil {
		t.Fatal(err)
	}
	unit := got.CleanupUnits[0]
	if !reflect.DeepEqual(unit.Regions, []string{"us-east-1"}) {
		t.Fatalf("regions = %#v", unit.Regions)
	}
	if unit.Evidence.ConfigName != "unknown" || unit.Decision != "needs_review" {
		t.Fatalf("unit = %+v", unit)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type recordingEngineReportWriter struct {
	input *s3.PutObjectInput
	err   error
}

func (writer *recordingEngineReportWriter) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	writer.input = input
	return &s3.PutObjectOutput{}, writer.err
}

func TestWriteEngineReportUsesImmutableIngestionKey(t *testing.T) {
	report, err := ToEngineReport(contractReport(contractCandidate(StateOrphan)))
	if err != nil {
		t.Fatal(err)
	}
	writer := &recordingEngineReportWriter{}
	key, err := WriteEngineReport(context.Background(), writer, "reaper-reports", report)
	if err != nil {
		t.Fatal(err)
	}
	wantKey := "engine-reports/harness/2026-08-08/" + report.ScanID + ".json"
	if key != wantKey || aws.ToString(writer.input.Key) != wantKey {
		t.Fatalf("key = %q input key = %q, want %q", key, aws.ToString(writer.input.Key), wantKey)
	}
	if aws.ToString(writer.input.IfNoneMatch) != "*" {
		t.Fatalf("IfNoneMatch = %q, want *", aws.ToString(writer.input.IfNoneMatch))
	}
	body, err := io.ReadAll(writer.input.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EngineReport
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ScanID != report.ScanID || decoded.CleanupUnits[0].FindingID != report.CleanupUnits[0].FindingID {
		t.Fatalf("uploaded report = %+v", decoded)
	}
}

func TestWriteEngineReportReturnsConditionalWriteFailure(t *testing.T) {
	report, err := ToEngineReport(contractReport(contractCandidate(StateOrphan)))
	if err != nil {
		t.Fatal(err)
	}
	writer := &recordingEngineReportWriter{err: errors.New("precondition failed")}
	if _, err := WriteEngineReport(context.Background(), writer, "reaper-reports", report); err == nil || !strings.Contains(err.Error(), "precondition failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestEphemeralHarnessStacksCarryEngineOwnershipTag(t *testing.T) {
	body, err := os.ReadFile("../../root.hcl")
	if err != nil {
		t.Fatal(err)
	}
	want := `%{if local.delete_after != ""}"reaper:engine" = "harness"%{endif}`
	if count := strings.Count(string(body), want); count != 1 {
		t.Fatalf("conditional ownership tag count = %d, want 1", count)
	}
}
