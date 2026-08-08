package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
)

func workerAction() ReaperActionRequest {
	return ReaperActionRequest{
		SchemaVersion:   1,
		ActionID:        "A1",
		Action:          "sweep",
		Engine:          "harness",
		Account:         contractAccount,
		ScanID:          "scan-1",
		FindingID:       "sha256:019406da27ce5a07617306ec9f386a2c4fd4d28b97e098426a6f8c3802aaafb8",
		ExpectedVersion: "sha256:cd817dda89287de462248e01e8e400cb6ed7853723643e4bff0417315457e90d",
		Identity:        "state-bucket/runs/bi-ha",
		Actor:           "U1",
		Reason:          "cleanup",
		RequestedAt:     900,
	}
}

type fakeWorkerIO struct {
	mu               sync.Mutex
	message          *ReceivedReaperAction
	states           []ReaperControlState
	loadCalls        int
	loadErrors       []error
	results          []ActionResult
	deleted          bool
	visibilityCalls  int
	visibilityValues []int32
	visibilityErrors []error
}

func newFakeWorkerIO(t *testing.T, states ...ReaperControlState) *fakeWorkerIO {
	t.Helper()
	body, err := json.Marshal(workerAction())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeWorkerIO{
		message: &ReceivedReaperAction{MessageID: "m1", ReceiptHandle: "receipt-1", Body: string(body)},
		states:  states,
	}
}

func (f *fakeWorkerIO) ReceiveAction(context.Context, string, int32) (*ReceivedReaperAction, error) {
	return f.message, nil
}

func (f *fakeWorkerIO) LoadControl(context.Context, string, ReaperActionRequest, int64) (ReaperControlState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.loadCalls
	f.loadCalls++
	if index < len(f.loadErrors) && f.loadErrors[index] != nil {
		return ReaperControlState{}, f.loadErrors[index]
	}
	if len(f.states) == 0 {
		return ReaperControlState{}, nil
	}
	if index >= len(f.states) {
		index = len(f.states) - 1
	}
	return f.states[index], nil
}

func (f *fakeWorkerIO) SendResult(_ context.Context, _ string, result ActionResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, result)
	return nil
}

func (f *fakeWorkerIO) DeleteAction(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = true
	return nil
}

func (f *fakeWorkerIO) ChangeActionVisibility(_ context.Context, _, _ string, visibility int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.visibilityCalls
	f.visibilityCalls++
	f.visibilityValues = append(f.visibilityValues, visibility)
	if index < len(f.visibilityErrors) {
		return f.visibilityErrors[index]
	}
	return nil
}

type fakeSelectedSweeper struct {
	candidate Candidate
	err       error
	prefixes  []string
	delay     time.Duration
	cancelled bool
	request   ReaperActionRequest
	veto      func(context.Context, ReaperActionRequest, Candidate) error
}

func (f *fakeSelectedSweeper) PrepareReaperAction(request ReaperActionRequest, veto func(context.Context, ReaperActionRequest, Candidate) error) {
	f.request = request
	f.veto = veto
}

func (f *fakeSelectedSweeper) SweepSelected(ctx context.Context, _ *Report, prefix, _ string) (Candidate, error) {
	f.prefixes = append(f.prefixes, prefix)
	if f.veto != nil {
		if err := f.veto(ctx, f.request, f.candidate); err != nil {
			return f.candidate, err
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			f.cancelled = true
			return Candidate{}, ctx.Err()
		}
	}
	return f.candidate, f.err
}

func queuedControl() ReaperControlState {
	return ReaperControlState{
		ActionExists: true, ActionStatus: "queued",
		ActiveActionID: "A1", ActiveExpiresAt: 22600,
	}
}

func runningControl() ReaperControlState {
	state := queuedControl()
	state.ActionStatus = "running"
	return state
}

func workerOptions() ReaperWorkerOptions {
	return ReaperWorkerOptions{
		ActionQueueURL: "actions", ResultQueueURL: "results", ControlTable: "control",
		AccountID: contractAccount, VisibilityTimeoutSeconds: 13500,
		PollTimeout: time.Second, PollInterval: time.Millisecond,
		Now: func() time.Time { return time.Unix(1000, 0).UTC() },
	}
}

func allowReaperAction(context.Context, ReaperActionRequest, Candidate) error { return nil }

func workerDeps(io ReaperWorkerIO, sweeper SelectedSweeper) ReaperWorkerDeps {
	return ReaperWorkerDeps{IO: io, Sweeper: sweeper, FinalVeto: allowReaperAction}
}

func TestReaperWorkerRequiresFinalControlPlaneVeto(t *testing.T) {
	io := newFakeWorkerIO(t, runningControl())
	_, err := ProcessReaperAction(
		context.Background(), ReaperWorkerDeps{IO: io, Sweeper: &fakeSelectedSweeper{}}, workerOptions(),
	)
	if err == nil || !strings.Contains(err.Error(), "final veto") || io.loadCalls != 0 {
		t.Fatalf("err=%v loadCalls=%d", err, io.loadCalls)
	}
}

func TestProcessSweepTargetsExactlyOnePrefix(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl(), runningControl())
	sweeper := &fakeSelectedSweeper{candidate: Candidate{State: StateOrphan, SweepResult: sweepResultDestroyed}}
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, sweeper), workerOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || len(sweeper.prefixes) != 1 || sweeper.prefixes[0] != "runs/bi-ha" {
		t.Fatalf("result=%+v prefixes=%#v", result, sweeper.prefixes)
	}
	if !io.deleted || len(io.results) != 2 || io.results[0].Status != "running" || io.results[1].Status != "succeeded" {
		t.Fatalf("deleted=%v results=%+v", io.deleted, io.results)
	}
}

func TestProcessSweepRejectsChangedFindingVersion(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl(), runningControl())
	sweeper := &fakeSelectedSweeper{err: ErrStale}
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, sweeper), workerOptions())
	if err != nil || result.Status != "stale" || !io.deleted {
		t.Fatalf("result=%+v err=%v deleted=%v", result, err, io.deleted)
	}
}

func TestProcessSweepRejectsNewActiveWorkflow(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl(), runningControl())
	sweeper := &fakeSelectedSweeper{err: ErrBlocked}
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, sweeper), workerOptions())
	if err != nil || result.Status != "blocked" || !io.deleted {
		t.Fatalf("result=%+v err=%v deleted=%v", result, err, io.deleted)
	}
}

func TestReaperWorkerRejectsHoldAddedAfterQueueing(t *testing.T) {
	state := queuedControl()
	state.HoldActive = true
	io := newFakeWorkerIO(t, state)
	sweeper := &fakeSelectedSweeper{}
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, sweeper), workerOptions())
	if err != nil || result.Status != "blocked" || len(sweeper.prefixes) != 0 || !io.deleted {
		t.Fatalf("result=%+v err=%v prefixes=%#v deleted=%v", result, err, sweeper.prefixes, io.deleted)
	}
}

func TestReaperWorkerFinalVetoSeesHoldAddedDuringFreshScan(t *testing.T) {
	candidate := selectedCandidate("runs/bi-ha/", StateOrphan)
	report := contractReport(candidate)
	var teardownCalls []string
	janitor := selectedJanitor(&report, &teardownCalls)
	hold := runningControl()
	hold.HoldActive = true
	io := newFakeWorkerIO(t, queuedControl(), runningControl(), hold)
	finalVeto := func(ctx context.Context, request ReaperActionRequest, _ Candidate) error {
		state, err := io.LoadControl(ctx, "control", request, 1000)
		if err != nil {
			return err
		}
		if state.HoldActive {
			return ErrBlocked
		}
		return nil
	}
	result, err := ProcessReaperAction(
		context.Background(),
		ReaperWorkerDeps{IO: io, Sweeper: janitor, FinalVeto: finalVeto},
		workerOptions(),
	)
	if err != nil || result.Status != "blocked" || len(teardownCalls) != 0 || !io.deleted {
		t.Fatalf("result=%+v err=%v teardown=%#v deleted=%v", result, err, teardownCalls, io.deleted)
	}
}

func TestReaperWorkerDoesNotRunCancelledAction(t *testing.T) {
	state := queuedControl()
	state.ActionStatus = "cancelled"
	io := newFakeWorkerIO(t, state)
	sweeper := &fakeSelectedSweeper{}
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, sweeper), workerOptions())
	if err != nil || result.Status != "cancelled" || len(sweeper.prefixes) != 0 || !io.deleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReaperWorkerRejectsExpiredOrMismatchedActiveMarker(t *testing.T) {
	for _, state := range []ReaperControlState{
		{ActionExists: true, ActionStatus: "queued", ActiveActionID: "A1", ActiveExpiresAt: 999},
		{ActionExists: true, ActionStatus: "queued", ActiveActionID: "other", ActiveExpiresAt: 22600},
		{ActionExists: false},
	} {
		io := newFakeWorkerIO(t, state)
		result, err := ProcessReaperAction(context.Background(), workerDeps(io, &fakeSelectedSweeper{}), workerOptions())
		if err != nil || result.Status != "blocked" || !io.deleted {
			t.Fatalf("state=%+v result=%+v err=%v deleted=%v", state, result, err, io.deleted)
		}
	}
}

func TestReaperWorkerMapsTerminalSweepOutcomes(t *testing.T) {
	cases := []struct {
		name      string
		candidate Candidate
		sweepErr  error
		want      string
	}{
		{"already gone", Candidate{}, ErrAlreadyGone, "already_gone"},
		{"teardown failed", Candidate{}, ErrTeardown, "failed"},
		{"residue", Candidate{State: StateResidue, Reason: residueIndexLagCaveat}, nil, "partial"},
		{"post destroy query failed", Candidate{State: StateUnknown, Reason: "old orphan reason", SweepResult: "destroy ran, but the post-destroy query failed"}, nil, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			io := newFakeWorkerIO(t, queuedControl(), runningControl())
			result, err := ProcessReaperAction(
				context.Background(),
				workerDeps(io, &fakeSelectedSweeper{candidate: tc.candidate, err: tc.sweepErr}),
				workerOptions(),
			)
			if err != nil || result.Status != tc.want || !io.deleted {
				t.Fatalf("result=%+v err=%v deleted=%v", result, err, io.deleted)
			}
			if tc.want == "partial" && result.Evidence["residueIndexLagCaveat"] != residueIndexLagCaveat {
				t.Fatalf("evidence=%#v", result.Evidence)
			}
			if tc.name == "post destroy query failed" && result.Message != tc.candidate.SweepResult {
				t.Fatalf("message=%q want=%q", result.Message, tc.candidate.SweepResult)
			}
			if tc.name == "post destroy query failed" && (result.Evidence["outcome"] != "inconclusive" || result.Evidence["severity"] != "alarm") {
				t.Fatalf("evidence=%#v", result.Evidence)
			}
		})
	}
}

func TestReaperWorkerAcknowledgesDuplicateAfterTerminalState(t *testing.T) {
	state := queuedControl()
	state.ActionStatus = "succeeded"
	state.ActiveActionID = ""
	io := newFakeWorkerIO(t, state)
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, &fakeSelectedSweeper{}), workerOptions())
	if err != nil || result.Status != "succeeded" || !io.deleted || len(io.results) != 0 {
		t.Fatalf("result=%+v err=%v deleted=%v results=%+v", result, err, io.deleted, io.results)
	}
}

func TestReaperWorkerRunsRedeliveredRunningActionWithFullLease(t *testing.T) {
	io := newFakeWorkerIO(t, runningControl())
	sweeper := &fakeSelectedSweeper{candidate: Candidate{State: StateOrphan, SweepResult: sweepResultDestroyed}}
	result, err := ProcessReaperAction(
		context.Background(), workerDeps(io, sweeper), workerOptions(),
	)
	if err != nil || result.Status != "succeeded" || len(io.results) != 1 || io.results[0].Status != "succeeded" || !io.deleted {
		t.Fatalf("result=%+v err=%v results=%+v deleted=%v", result, err, io.results, io.deleted)
	}
}

func TestReaperWorkerBlocksRunningRedeliveryWithoutFullExecutionLease(t *testing.T) {
	state := runningControl()
	state.ActiveExpiresAt = 1001
	io := newFakeWorkerIO(t, state)
	sweeper := &fakeSelectedSweeper{}
	result, err := ProcessReaperAction(
		context.Background(), workerDeps(io, sweeper), workerOptions(),
	)
	if err != nil || result.Status != "blocked" || len(sweeper.prefixes) != 0 || !io.deleted {
		t.Fatalf("result=%+v err=%v prefixes=%#v deleted=%v", result, err, sweeper.prefixes, io.deleted)
	}
}

func TestReaperWorkerVisibilityLossIsAlarmLevelAndNeverDeletesRequest(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl(), runningControl())
	io.visibilityErrors = []error{errors.New("temporary"), ErrMessageNotInflight}
	sweeper := &fakeSelectedSweeper{candidate: Candidate{State: StateOrphan, SweepResult: sweepResultDestroyed}, delay: 30 * time.Millisecond}
	opts := workerOptions()
	opts.HeartbeatInterval = time.Millisecond
	opts.HeartbeatRetryInterval = time.Millisecond
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, sweeper), opts)
	if err != nil || result.Status != "visibility_lost" || io.deleted || !sweeper.cancelled {
		t.Fatalf("result=%+v err=%v deleted=%v cancelled=%v", result, err, io.deleted, sweeper.cancelled)
	}
	if got := io.results[len(io.results)-1].Status; got != "visibility_lost" {
		t.Fatalf("last result status = %q, results=%+v", got, io.results)
	}
}

func TestReaperWorkerRetriesTransientHeartbeatUntilVisibilityIsActuallyLost(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl(), runningControl())
	io.visibilityErrors = []error{errors.New("temporary one"), errors.New("temporary two")}
	sweeper := &fakeSelectedSweeper{candidate: Candidate{State: StateOrphan, SweepResult: sweepResultDestroyed}, delay: 10 * time.Millisecond}
	opts := workerOptions()
	opts.HeartbeatInterval = time.Millisecond
	opts.HeartbeatRetryInterval = time.Millisecond
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, sweeper), opts)
	if err != nil || result.Status != "succeeded" || io.visibilityCalls < 3 || io.deleted == false {
		t.Fatalf("result=%+v err=%v visibilityCalls=%d deleted=%v", result, err, io.visibilityCalls, io.deleted)
	}
}

func TestReaperWorkerHeartbeatCancellationIsNotVisibilityLoss(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl(), runningControl())
	io.visibilityErrors = []error{errors.New("temporary")}
	sweeper := &fakeSelectedSweeper{candidate: Candidate{State: StateOrphan, SweepResult: sweepResultDestroyed}, delay: 3 * time.Millisecond}
	opts := workerOptions()
	opts.HeartbeatInterval = time.Millisecond
	opts.HeartbeatRetryInterval = time.Hour
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, sweeper), opts)
	if err != nil || result.Status != "succeeded" || !io.deleted {
		t.Fatalf("result=%+v err=%v deleted=%v results=%+v", result, err, io.deleted, io.results)
	}
}

func TestReaperWorkerHeartbeatRunsBeforeHalfTheVisibilityWindow(t *testing.T) {
	opts := (ReaperWorkerOptions{VisibilityTimeoutSeconds: 13500}).defaults()
	half := time.Duration(opts.VisibilityTimeoutSeconds) * time.Second / 2
	if opts.HeartbeatInterval <= 0 || opts.HeartbeatInterval >= half {
		t.Fatalf("heartbeat=%s half=%s", opts.HeartbeatInterval, half)
	}
}

func TestReaperWorkerInvalidMessageIsReleasedQuicklyForDLQ(t *testing.T) {
	io := newFakeWorkerIO(t)
	io.message.Body = `{"schema_version":2}`
	_, err := ProcessReaperAction(context.Background(), workerDeps(io, &fakeSelectedSweeper{}), workerOptions())
	if err == nil || io.deleted || len(io.results) != 0 || len(io.visibilityValues) != 1 || io.visibilityValues[0] != 30 {
		t.Fatalf("err=%v deleted=%v results=%+v visibility=%#v", err, io.deleted, io.results, io.visibilityValues)
	}
}

func TestReaperWorkerTreatsControlMismatchAsTerminalBlocked(t *testing.T) {
	io := newFakeWorkerIO(t)
	io.loadErrors = []error{fmt.Errorf("%w: actor", ErrControlMismatch)}
	result, err := ProcessReaperAction(context.Background(), workerDeps(io, &fakeSelectedSweeper{}), workerOptions())
	if err != nil || result.Status != "blocked" || !io.deleted || len(io.visibilityValues) != 0 {
		t.Fatalf("result=%+v err=%v deleted=%v visibility=%#v", result, err, io.deleted, io.visibilityValues)
	}
}

func TestReaperWorkerReleasesMessageAfterRunningPollTimeout(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl())
	opts := workerOptions()
	opts.PollTimeout = 5 * time.Millisecond
	_, err := ProcessReaperAction(
		context.Background(), workerDeps(io, &fakeSelectedSweeper{}), opts,
	)
	if err == nil || io.deleted || len(io.visibilityValues) == 0 || io.visibilityValues[len(io.visibilityValues)-1] != 0 {
		t.Fatalf("err=%v deleted=%v visibility=%#v", err, io.deleted, io.visibilityValues)
	}
}

func TestReaperWorkerRequiresTheCommittedSixHourRunningLease(t *testing.T) {
	short := runningControl()
	short.ActiveExpiresAt--
	io := newFakeWorkerIO(t, queuedControl(), short)
	opts := workerOptions()
	opts.PollTimeout = 5 * time.Millisecond
	_, err := ProcessReaperAction(
		context.Background(), workerDeps(io, &fakeSelectedSweeper{}), opts,
	)
	if err == nil || io.deleted || len(io.visibilityValues) == 0 || io.visibilityValues[len(io.visibilityValues)-1] != 0 {
		t.Fatalf("err=%v deleted=%v visibility=%#v", err, io.deleted, io.visibilityValues)
	}
}

func TestReaperWorkerHonorsCancellationWhileWaitingForRunning(t *testing.T) {
	cancelled := queuedControl()
	cancelled.ActionStatus = "cancelled"
	io := newFakeWorkerIO(t, queuedControl(), cancelled)
	sweeper := &fakeSelectedSweeper{}
	result, err := ProcessReaperAction(
		context.Background(), workerDeps(io, sweeper), workerOptions(),
	)
	if err != nil || result.Status != "cancelled" || len(sweeper.prefixes) != 0 || !io.deleted {
		t.Fatalf("result=%+v err=%v prefixes=%#v deleted=%v", result, err, sweeper.prefixes, io.deleted)
	}
}

func TestReaperWorkerReleasesMessageAfterPreTeardownScanFailure(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl(), runningControl())
	sweeper := &fakeSelectedSweeper{err: fmt.Errorf("%w: scan unavailable", ErrRetryable)}
	_, err := ProcessReaperAction(
		context.Background(), workerDeps(io, sweeper), workerOptions(),
	)
	if err == nil || io.deleted || len(io.visibilityValues) == 0 || io.visibilityValues[len(io.visibilityValues)-1] != 0 {
		t.Fatalf("err=%v deleted=%v visibility=%#v", err, io.deleted, io.visibilityValues)
	}
}

func selectedCandidate(prefix string, state CandidateState) Candidate {
	candidate := contractCandidate(state)
	candidate.Prefix = prefix
	candidate.ConfigName = "min_default"
	candidate.Identifier = "smokeaa-min"
	candidate.Customer = "smokeaa"
	return candidate
}

func selectedJanitor(report *Report, teardownCalls *[]string) *Janitor {
	return &Janitor{
		Deps: JanitorDeps{
			Tags: map[string]TagAPI{
				"us-east-1": &fakeTagAPI{},
				"us-west-2": &fakeTagAPI{},
			},
			Matrix: testMatrix(),
			Teardown: func(_ context.Context, params PhaseParams, _ bool) error {
				*teardownCalls = append(*teardownCalls, params.RunID)
				return nil
			},
		},
		Options: testOptions(time.Unix(1000, 0).UTC()),
		ScanFunc: func(context.Context, JanitorDeps, JanitorOptions, JanitorWorkflowList) (*Report, error) {
			copy := *report
			copy.Candidates = append([]Candidate(nil), report.Candidates...)
			return &copy, nil
		},
	}
}

func TestSweepSelectedRejectsChangedFindingVersion(t *testing.T) {
	report := contractReport(selectedCandidate("runs/bi-ha/", StateOrphan))
	var teardownCalls []string
	janitor := selectedJanitor(&report, &teardownCalls)
	_, err := janitor.SweepSelected(context.Background(), &Report{}, "runs/bi-ha", "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if !errors.Is(err, ErrStale) || len(teardownCalls) != 0 {
		t.Fatalf("err=%v teardown=%#v", err, teardownCalls)
	}
}

func TestSweepSelectedRejectsNewActiveWorkflow(t *testing.T) {
	candidate := selectedCandidate("runs/bi-ha/", StateActive)
	report := contractReport(candidate)
	engineReport, err := ToEngineReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var teardownCalls []string
	janitor := selectedJanitor(&report, &teardownCalls)
	_, err = janitor.SweepSelected(context.Background(), &Report{}, "runs/bi-ha", engineReport.CleanupUnits[0].Version)
	if !errors.Is(err, ErrBlocked) || len(teardownCalls) != 0 {
		t.Fatalf("err=%v teardown=%#v", err, teardownCalls)
	}
}

func TestSweepSelectedTargetsExactlyOnePrefix(t *testing.T) {
	target := selectedCandidate("runs/run-a/", StateOrphan)
	target.RunID = "run-a"
	other := selectedCandidate("runs/run-b/", StateOrphan)
	other.RunID = "run-b"
	report := contractReport(target)
	report.Candidates = []Candidate{target, other}
	engineReport, err := ToEngineReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var teardownCalls []string
	janitor := selectedJanitor(&report, &teardownCalls)
	var selected Report
	candidate, err := janitor.SweepSelected(context.Background(), &selected, "runs/run-a", engineReport.CleanupUnits[0].Version)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.SweepResult != sweepResultDestroyed || len(teardownCalls) != 1 || teardownCalls[0] != "run-a" || len(selected.Candidates) != 1 {
		t.Fatalf("candidate=%+v teardown=%#v report=%+v", candidate, teardownCalls, selected)
	}
}

func TestSweepSelectedRunsFinalVetoAfterFreshScan(t *testing.T) {
	candidate := selectedCandidate("runs/bi-ha/", StateOrphan)
	report := contractReport(candidate)
	engineReport, err := ToEngineReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var teardownCalls []string
	janitor := selectedJanitor(&report, &teardownCalls)
	janitor.FinalVeto = func(context.Context, Candidate) error { return ErrBlocked }
	_, err = janitor.SweepSelected(context.Background(), &Report{}, "runs/bi-ha", engineReport.CleanupUnits[0].Version)
	if !errors.Is(err, ErrBlocked) || len(teardownCalls) != 0 {
		t.Fatalf("err=%v teardown=%#v", err, teardownCalls)
	}
}

func TestSweepSelectedRejectsProtectedHeldAndKeptCandidates(t *testing.T) {
	cases := []Candidate{
		selectedCandidate("runs/active/", StateBlocked),
		selectedCandidate("runs/kept/", StateKept),
		selectedCandidate("runs/protected/", StateOrphan),
	}
	cases[2].Identifier = "dev-min"
	for _, candidate := range cases {
		report := contractReport(candidate)
		engineReport, err := ToEngineReport(report)
		if err != nil {
			t.Fatal(err)
		}
		var teardownCalls []string
		janitor := selectedJanitor(&report, &teardownCalls)
		_, err = janitor.SweepSelected(
			context.Background(), &Report{}, candidate.Prefix,
			engineReport.CleanupUnits[0].Version,
		)
		if !errors.Is(err, ErrBlocked) || len(teardownCalls) != 0 {
			t.Fatalf("candidate=%+v err=%v teardown=%#v", candidate, err, teardownCalls)
		}
	}
}

func TestReaperWorkerDrainsOnlyCancelledActions(t *testing.T) {
	state := queuedControl()
	state.ActionStatus = "cancelled"
	io := newFakeWorkerIO(t, state)
	archive := &recordingEngineReportWriter{}
	result, err := DrainCancelledReaperAction(
		context.Background(), io, archive, "reaper-reports", workerOptions(),
	)
	if err != nil || result.Status != "cancelled" || !io.deleted {
		t.Fatalf("result=%+v err=%v deleted=%v", result, err, io.deleted)
	}
	if archive.input == nil || *archive.input.Key != "cancelled-actions/A1.json" || *archive.input.IfNoneMatch != "*" {
		t.Fatalf("archive input=%+v", archive.input)
	}
	if len(io.results) != 1 || io.results[0].Status != "cancelled" {
		t.Fatalf("results=%+v", io.results)
	}
}

func TestReaperWorkerDrainLeavesNonCancelledActionInQueue(t *testing.T) {
	io := newFakeWorkerIO(t, queuedControl())
	archive := &recordingEngineReportWriter{}
	_, err := DrainCancelledReaperAction(
		context.Background(), io, archive, "reaper-reports", workerOptions(),
	)
	if err == nil || io.deleted || archive.input != nil || len(io.results) != 0 || len(io.visibilityValues) == 0 || io.visibilityValues[len(io.visibilityValues)-1] != 0 {
		t.Fatalf("err=%v deleted=%v archive=%+v results=%+v visibility=%#v", err, io.deleted, archive.input, io.results, io.visibilityValues)
	}
}

type fakeAWSWorkerQueue struct {
	sent            *sqs.SendMessageInput
	received        *sqs.ReceiveMessageInput
	visibilityError error
}

func (f *fakeAWSWorkerQueue) ReceiveMessage(_ context.Context, input *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.received = input
	return &sqs.ReceiveMessageOutput{}, nil
}

func (f *fakeAWSWorkerQueue) SendMessage(_ context.Context, input *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.sent = input
	return &sqs.SendMessageOutput{}, nil
}

func (f *fakeAWSWorkerQueue) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return &sqs.DeleteMessageOutput{}, nil
}

func (f *fakeAWSWorkerQueue) ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	return &sqs.ChangeMessageVisibilityOutput{}, f.visibilityError
}

type fakeAWSWorkerControl struct {
	items  map[string]map[string]ddbtypes.AttributeValue
	inputs []*dynamodb.GetItemInput
}

func (f *fakeAWSWorkerControl) GetItem(_ context.Context, input *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.inputs = append(f.inputs, input)
	pk := input.Key["PK"].(*ddbtypes.AttributeValueMemberS).Value
	return &dynamodb.GetItemOutput{Item: f.items[pk]}, nil
}

func stringItem(values map[string]string) map[string]ddbtypes.AttributeValue {
	item := map[string]ddbtypes.AttributeValue{}
	for key, value := range values {
		item[key] = &ddbtypes.AttributeValueMemberS{Value: value}
	}
	return item
}

func TestAWSReaperWorkerIOReadsExactConsistentControlKeys(t *testing.T) {
	request := workerAction()
	action := stringItem(map[string]string{
		"action_id": request.ActionID, "action": request.Action,
		"engine": request.Engine, "account": request.Account,
		"scan_id": request.ScanID, "finding_id": request.FindingID,
		"expected_version": request.ExpectedVersion, "identity": request.Identity,
		"actor": request.Actor, "reason": request.Reason,
		"status": "running",
	})
	action["requested_at"] = &ddbtypes.AttributeValueMemberN{Value: "900"}
	active := stringItem(map[string]string{"action_id": request.ActionID})
	active["ttl_epoch_seconds"] = &ddbtypes.AttributeValueMemberN{Value: "22600"}
	control := &fakeAWSWorkerControl{items: map[string]map[string]ddbtypes.AttributeValue{
		"ACTION#A1":                           action,
		"ACTIVE#harness#" + request.FindingID: active,
		"HOLD#harness#" + request.FindingID:   {},
	}}
	adapter := &AWSReaperWorkerIO{Queue: &fakeAWSWorkerQueue{}, Control: control}
	state, err := adapter.LoadControl(context.Background(), "control", request, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActionStatus != "running" || state.ActiveExpiresAt != 22600 || state.HoldActive {
		t.Fatalf("state=%+v", state)
	}
	if len(control.inputs) != 3 {
		t.Fatalf("GetItem calls=%d", len(control.inputs))
	}
	for _, input := range control.inputs {
		if !aws.ToBool(input.ConsistentRead) || aws.ToString(input.TableName) != "control" {
			t.Fatalf("input=%+v", input)
		}
	}
}

func TestAWSReaperWorkerIOSendsFIFOResultIdentifiers(t *testing.T) {
	queue := &fakeAWSWorkerQueue{}
	adapter := &AWSReaperWorkerIO{Queue: queue}
	result := workerResult(workerOptions(), workerAction(), "running", "started")
	if err := adapter.SendResult(context.Background(), "results", result); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(queue.sent.MessageGroupId) != result.FindingID || aws.ToString(queue.sent.MessageDeduplicationId) != "A1:running" {
		t.Fatalf("send=%+v", queue.sent)
	}
}

func TestAWSReaperWorkerIOUsesLongPolling(t *testing.T) {
	queue := &fakeAWSWorkerQueue{}
	adapter := &AWSReaperWorkerIO{Queue: queue}
	if _, err := adapter.ReceiveAction(context.Background(), "actions", 13500); err != nil {
		t.Fatal(err)
	}
	if queue.received.WaitTimeSeconds < 10 || queue.received.MaxNumberOfMessages != 1 {
		t.Fatalf("receive=%+v", queue.received)
	}
}

func TestAWSReaperWorkerIORejectsControlRecordMismatch(t *testing.T) {
	request := workerAction()
	action := stringItem(map[string]string{
		"action_id": request.ActionID, "action": request.Action,
		"engine": request.Engine, "account": request.Account,
		"scan_id": request.ScanID, "finding_id": request.FindingID,
		"expected_version": request.ExpectedVersion, "identity": "different-bucket/runs/bi-ha",
		"actor": request.Actor, "reason": request.Reason,
	})
	control := &fakeAWSWorkerControl{items: map[string]map[string]ddbtypes.AttributeValue{"ACTION#A1": action}}
	adapter := &AWSReaperWorkerIO{Queue: &fakeAWSWorkerQueue{}, Control: control}
	if _, err := adapter.LoadControl(context.Background(), "control", request, 1000); !errors.Is(err, ErrControlMismatch) {
		t.Fatalf("mismatched control record error=%v", err)
	}
}

func TestValidateReaperActionRejectsAccountAndFindingIdentityMismatch(t *testing.T) {
	request := workerAction()
	request.Account = "000000000000"
	if err := validateReaperAction(request, contractAccount); err == nil {
		t.Fatal("wrong account was accepted")
	}
	request = workerAction()
	request.Identity = "state-bucket/runs/other"
	if err := validateReaperAction(request, contractAccount); err == nil {
		t.Fatal("finding id mismatch was accepted")
	}
}

func TestAWSReaperWorkerIOClassifiesLostReceiptHandle(t *testing.T) {
	queue := &fakeAWSWorkerQueue{visibilityError: &smithy.GenericAPIError{Code: "MessageNotInflight", Message: "gone"}}
	adapter := &AWSReaperWorkerIO{Queue: queue}
	err := adapter.ChangeActionVisibility(context.Background(), "actions", "receipt", 13500)
	if !errors.Is(err, ErrMessageNotInflight) {
		t.Fatalf("err=%v", err)
	}
}
