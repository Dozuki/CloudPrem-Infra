package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
)

var (
	ErrStale                = errors.New("resource reaper action is stale")
	ErrBlocked              = errors.New("resource reaper action is blocked")
	ErrAlreadyGone          = errors.New("resource reaper target is already gone")
	ErrTeardown             = errors.New("resource reaper teardown failed")
	ErrMessageNotInflight   = errors.New("resource reaper request is no longer in flight")
	ErrControlMismatch      = errors.New("resource reaper control record does not match request")
	ErrRetryable            = errors.New("resource reaper action should be retried")
	reaperWorkerHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reaperActionIDPattern   = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
)

const activeActionLeaseSeconds int64 = 21600

type ReaperActionRequest struct {
	SchemaVersion   int    `json:"schema_version"`
	ActionID        string `json:"action_id"`
	Action          string `json:"action"`
	Engine          string `json:"engine"`
	Account         string `json:"account"`
	ScanID          string `json:"scan_id"`
	FindingID       string `json:"finding_id"`
	ExpectedVersion string `json:"expected_version"`
	Identity        string `json:"identity"`
	Actor           string `json:"actor"`
	Reason          string `json:"reason"`
	RequestedAt     int64  `json:"requested_at"`
}

type ActionResult struct {
	SchemaVersion int               `json:"schema_version"`
	ActionID      string            `json:"action_id"`
	EventType     string            `json:"event_type"`
	Status        string            `json:"status"`
	Engine        string            `json:"engine"`
	FindingID     string            `json:"finding_id"`
	OccurredAt    int64             `json:"occurred_at"`
	Message       string            `json:"message"`
	Evidence      map[string]string `json:"evidence,omitempty"`
}

type ReceivedReaperAction struct {
	MessageID     string
	ReceiptHandle string
	Body          string
}

type ReaperControlState struct {
	ActionExists    bool
	ActionStatus    string
	ActiveActionID  string
	ActiveExpiresAt int64
	HoldActive      bool
}

type ReaperWorkerIO interface {
	ReceiveAction(context.Context, string, int32) (*ReceivedReaperAction, error)
	LoadControl(context.Context, string, ReaperActionRequest, int64) (ReaperControlState, error)
	SendResult(context.Context, string, ActionResult) error
	DeleteAction(context.Context, string, string) error
	ChangeActionVisibility(context.Context, string, string, int32) error
}

type ReaperSQSAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

type ReaperDynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}

type AWSReaperWorkerIO struct {
	Queue   ReaperSQSAPI
	Control ReaperDynamoAPI
}

func (adapter *AWSReaperWorkerIO) ReceiveAction(ctx context.Context, queueURL string, visibility int32) (*ReceivedReaperAction, error) {
	output, err := adapter.Queue.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queueURL),
		MaxNumberOfMessages: 1,
		VisibilityTimeout:   visibility,
		WaitTimeSeconds:     20,
	})
	if err != nil {
		return nil, err
	}
	if len(output.Messages) == 0 {
		return nil, nil
	}
	message := output.Messages[0]
	return &ReceivedReaperAction{
		MessageID:     aws.ToString(message.MessageId),
		ReceiptHandle: aws.ToString(message.ReceiptHandle),
		Body:          aws.ToString(message.Body),
	}, nil
}

func dynamoString(item map[string]ddbtypes.AttributeValue, key string) string {
	if value, ok := item[key].(*ddbtypes.AttributeValueMemberS); ok {
		return value.Value
	}
	return ""
}

func dynamoInt64(item map[string]ddbtypes.AttributeValue, key string) int64 {
	if value, ok := item[key].(*ddbtypes.AttributeValueMemberN); ok {
		parsed, _ := strconv.ParseInt(value.Value, 10, 64)
		return parsed
	}
	return 0
}

func (adapter *AWSReaperWorkerIO) getControlItem(ctx context.Context, table, pk string) (map[string]ddbtypes.AttributeValue, error) {
	output, err := adapter.Control.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(table),
		ConsistentRead: aws.Bool(true),
		Key: map[string]ddbtypes.AttributeValue{
			"PK": &ddbtypes.AttributeValueMemberS{Value: pk},
			"SK": &ddbtypes.AttributeValueMemberS{Value: "META"},
		},
	})
	if err != nil {
		return nil, err
	}
	return output.Item, nil
}

func (adapter *AWSReaperWorkerIO) LoadControl(ctx context.Context, table string, request ReaperActionRequest, now int64) (ReaperControlState, error) {
	action, err := adapter.getControlItem(ctx, table, "ACTION#"+request.ActionID)
	if err != nil {
		return ReaperControlState{}, err
	}
	active, err := adapter.getControlItem(ctx, table, "ACTIVE#harness#"+request.FindingID)
	if err != nil {
		return ReaperControlState{}, err
	}
	hold, err := adapter.getControlItem(ctx, table, "HOLD#harness#"+request.FindingID)
	if err != nil {
		return ReaperControlState{}, err
	}
	state := ReaperControlState{
		ActionExists:    len(action) > 0,
		ActionStatus:    dynamoString(action, "status"),
		ActiveActionID:  dynamoString(active, "action_id"),
		ActiveExpiresAt: dynamoInt64(active, "ttl_epoch_seconds"),
	}
	if len(action) > 0 {
		for field, expected := range map[string]string{
			"action_id": request.ActionID, "action": request.Action,
			"engine": request.Engine, "account": request.Account,
			"scan_id": request.ScanID, "finding_id": request.FindingID,
			"expected_version": request.ExpectedVersion, "identity": request.Identity,
			"actor": request.Actor, "reason": request.Reason,
		} {
			if dynamoString(action, field) != expected {
				return ReaperControlState{}, fmt.Errorf("%w: action field %s", ErrControlMismatch, field)
			}
		}
		if dynamoInt64(action, "requested_at") != request.RequestedAt {
			return ReaperControlState{}, fmt.Errorf("%w: action field requested_at", ErrControlMismatch)
		}
	}
	if len(hold) > 0 {
		expires := dynamoInt64(hold, "expires_at")
		state.HoldActive = expires == 0 || expires > now
	}
	return state, nil
}

func (adapter *AWSReaperWorkerIO) SendResult(ctx context.Context, queueURL string, result ActionResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = adapter.Queue.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(queueURL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String(result.FindingID),
		MessageDeduplicationId: aws.String(result.ActionID + ":" + result.EventType),
	})
	return err
}

func (adapter *AWSReaperWorkerIO) DeleteAction(ctx context.Context, queueURL, receiptHandle string) error {
	_, err := adapter.Queue.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle),
	})
	return err
}

func (adapter *AWSReaperWorkerIO) ChangeActionVisibility(ctx context.Context, queueURL, receiptHandle string, visibility int32) error {
	_, err := adapter.Queue.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle),
		VisibilityTimeout: visibility,
	})
	if err == nil {
		return nil
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "MessageNotInflight", "ReceiptHandleIsInvalid", "InvalidParameterValue":
			return fmt.Errorf("%w: %s", ErrMessageNotInflight, apiError.ErrorMessage())
		}
	}
	return err
}

type SelectedSweeper interface {
	SweepSelected(context.Context, *Report, string, string) (Candidate, error)
}

type ReaperWorkerDeps struct {
	IO        ReaperWorkerIO
	Sweeper   SelectedSweeper
	FinalVeto func(context.Context, ReaperActionRequest, Candidate) error
}

type actionPreparedSweeper interface {
	PrepareReaperAction(ReaperActionRequest, func(context.Context, ReaperActionRequest, Candidate) error)
}

type ReaperWorkerOptions struct {
	ActionQueueURL           string
	ResultQueueURL           string
	ControlTable             string
	AccountID                string
	VisibilityTimeoutSeconds int32
	PollTimeout              time.Duration
	PollInterval             time.Duration
	HeartbeatInterval        time.Duration
	HeartbeatRetryInterval   time.Duration
	Now                      func() time.Time
}

func (options ReaperWorkerOptions) now() time.Time {
	if options.Now != nil {
		return options.Now().UTC()
	}
	return time.Now().UTC()
}

func (options ReaperWorkerOptions) defaults() ReaperWorkerOptions {
	if options.VisibilityTimeoutSeconds <= 0 {
		options.VisibilityTimeoutSeconds = 13500
	}
	if options.PollTimeout <= 0 {
		options.PollTimeout = 60 * time.Second
	}
	if options.PollInterval <= 0 {
		options.PollInterval = time.Second
	}
	if options.HeartbeatInterval <= 0 {
		half := time.Duration(options.VisibilityTimeoutSeconds) * time.Second / 2
		options.HeartbeatInterval = half - time.Minute
		if options.HeartbeatInterval <= 0 {
			options.HeartbeatInterval = half / 2
		}
	}
	if options.HeartbeatRetryInterval <= 0 {
		options.HeartbeatRetryInterval = 30 * time.Second
	}
	return options
}

func validateReaperAction(request ReaperActionRequest, account string) error {
	if request.SchemaVersion != 1 {
		return fmt.Errorf("unsupported action schema_version %d", request.SchemaVersion)
	}
	if request.Engine != "harness" || request.Action != "sweep" {
		return fmt.Errorf("unsupported engine/action %q/%q", request.Engine, request.Action)
	}
	if request.Account != account || !engineReportAccountPattern.MatchString(request.Account) {
		return fmt.Errorf("action account %q does not match worker account", request.Account)
	}
	if request.ActionID == "" || request.ScanID == "" || request.Identity == "" || request.Actor == "" || strings.TrimSpace(request.Reason) == "" {
		return fmt.Errorf("action identity, actor, and reason are required")
	}
	if !reaperActionIDPattern.MatchString(request.ActionID) || len(request.ScanID) > 128 || len([]rune(request.Reason)) > 2000 {
		return fmt.Errorf("action_id, scan_id, or reason is invalid or too long")
	}
	if !reaperWorkerHashPattern.MatchString(request.FindingID) || !reaperWorkerHashPattern.MatchString(request.ExpectedVersion) {
		return fmt.Errorf("action finding_id or expected_version is invalid")
	}
	if request.FindingID != FindingID("harness", request.Account, request.Identity) {
		return fmt.Errorf("action finding_id does not match account and identity")
	}
	if err := rejectNUL(
		"action",
		request.ActionID,
		request.ScanID,
		request.Identity,
		request.Actor,
		request.Reason,
	); err != nil {
		return err
	}
	if request.RequestedAt < 0 {
		return fmt.Errorf("requested_at must be non-negative")
	}
	if _, err := actionStatePrefix(request.Identity); err != nil {
		return err
	}
	return nil
}

func actionStatePrefix(identity string) (string, error) {
	separator := strings.Index(identity, "/")
	if separator <= 0 || separator == len(identity)-1 {
		return "", fmt.Errorf("action identity must contain state bucket and prefix")
	}
	prefix := strings.Trim(identity[separator+1:], "/")
	if prefix == "" {
		return "", fmt.Errorf("action identity state prefix is empty")
	}
	return prefix, nil
}

func workerResult(options ReaperWorkerOptions, request ReaperActionRequest, status, message string) ActionResult {
	if message == "" {
		message = "Harness action update."
	}
	if runes := []rune(message); len(runes) > 2000 {
		message = string(runes[:2000])
	}
	return ActionResult{
		SchemaVersion: 1,
		ActionID:      request.ActionID,
		EventType:     status,
		Status:        status,
		Engine:        "harness",
		FindingID:     request.FindingID,
		OccurredAt:    options.now().Unix(),
		Message:       message,
	}
}

func sendTerminalAndDelete(
	ctx context.Context,
	io ReaperWorkerIO,
	options ReaperWorkerOptions,
	request ReaperActionRequest,
	message *ReceivedReaperAction,
	result ActionResult,
) (ActionResult, error) {
	if err := io.SendResult(ctx, options.ResultQueueURL, result); err != nil {
		return ActionResult{}, fmt.Errorf("send %s result: %w", result.Status, err)
	}
	if err := io.DeleteAction(ctx, options.ActionQueueURL, message.ReceiptHandle); err != nil {
		return ActionResult{}, fmt.Errorf("delete completed action request: %w", err)
	}
	return result, nil
}

func releaseActionForRetry(ctx context.Context, io ReaperWorkerIO, options ReaperWorkerOptions, message *ReceivedReaperAction, cause error) error {
	return deferActionForRetry(ctx, io, options, message, 0, cause)
}

func deferActionForRetry(ctx context.Context, io ReaperWorkerIO, options ReaperWorkerOptions, message *ReceivedReaperAction, visibility int32, cause error) error {
	if err := io.ChangeActionVisibility(ctx, options.ActionQueueURL, message.ReceiptHandle, visibility); err != nil {
		return errors.Join(cause, fmt.Errorf("release action request for retry: %w", err))
	}
	return cause
}

func activeControl(state ReaperControlState, request ReaperActionRequest, now int64) bool {
	return state.ActionExists &&
		state.ActiveActionID == request.ActionID &&
		state.ActiveExpiresAt >= now
}

func executionLeaseReady(state ReaperControlState, request ReaperActionRequest, now int64) bool {
	return activeControl(state, request, now) &&
		state.ActiveExpiresAt >= now+int64(JanitorPodActiveDeadlineSeconds)
}

var terminalActionStatuses = map[string]bool{
	"succeeded": true, "partial": true, "blocked": true, "stale": true,
	"failed": true, "cancelled": true, "already_gone": true, "visibility_lost": true,
}

func waitForRunning(
	ctx context.Context,
	io ReaperWorkerIO,
	options ReaperWorkerOptions,
	request ReaperActionRequest,
	runningAt int64,
) (ReaperControlState, error) {
	deadline := time.NewTimer(options.PollTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(options.PollInterval)
	defer ticker.Stop()
	for {
		state, err := io.LoadControl(ctx, options.ControlTable, request, options.now().Unix())
		if err != nil {
			return ReaperControlState{}, err
		}
		if state.ActionStatus == "cancelled" || state.HoldActive {
			return state, nil
		}
		if state.ActionStatus == "running" && activeControl(state, request, options.now().Unix()) && state.ActiveExpiresAt >= runningAt+activeActionLeaseSeconds {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return ReaperControlState{}, ctx.Err()
		case <-deadline.C:
			return ReaperControlState{}, fmt.Errorf("timed out waiting for committed running action")
		case <-ticker.C:
		}
	}
}

type heartbeatOutcome struct {
	result ActionResult
	err    error
}

func startVisibilityHeartbeat(
	ctx context.Context,
	io ReaperWorkerIO,
	options ReaperWorkerOptions,
	request ReaperActionRequest,
	message *ReceivedReaperAction,
	cancelSweep context.CancelFunc,
) (context.CancelFunc, <-chan heartbeatOutcome) {
	heartbeatContext, cancel := context.WithCancel(ctx)
	finished := make(chan heartbeatOutcome, 1)
	go func() {
		timer := time.NewTimer(options.HeartbeatInterval)
		defer timer.Stop()
		visibleUntil := time.Now().Add(time.Duration(options.VisibilityTimeoutSeconds) * time.Second)
		for {
			select {
			case <-heartbeatContext.Done():
				finished <- heartbeatOutcome{}
				return
			case <-timer.C:
				err := io.ChangeActionVisibility(
					heartbeatContext,
					options.ActionQueueURL,
					message.ReceiptHandle,
					options.VisibilityTimeoutSeconds,
				)
				if heartbeatContext.Err() != nil {
					finished <- heartbeatOutcome{}
					return
				}
				if err == nil {
					visibleUntil = time.Now().Add(time.Duration(options.VisibilityTimeoutSeconds) * time.Second)
					timer.Reset(options.HeartbeatInterval)
					continue
				}
				// A transient API error is not visibility loss. Keep retrying while the
				// last known visibility grant still has a one-minute safety margin.
				if !errors.Is(err, ErrMessageNotInflight) && time.Until(visibleUntil) > time.Minute+options.HeartbeatRetryInterval {
					timer.Reset(options.HeartbeatRetryInterval)
					continue
				}
				cancelSweep()
				result := workerResult(options, request, "visibility_lost", "Action request visibility was lost while teardown was running; outcome requires review.")
				result.Evidence = map[string]string{
					"severity": "alarm", "outcome": "inconclusive", "error": err.Error(),
					"teardown": "cancellation_requested",
				}
				sendErr := io.SendResult(context.WithoutCancel(ctx), options.ResultQueueURL, result)
				finished <- heartbeatOutcome{result: result, err: sendErr}
				return
			}
		}
	}()
	return cancel, finished
}

func mapSweepResult(options ReaperWorkerOptions, request ReaperActionRequest, candidate Candidate, sweepErr error) (ActionResult, error) {
	switch {
	case errors.Is(sweepErr, ErrStale):
		return workerResult(options, request, "stale", "Harness finding changed after the action was requested; nothing was destroyed."), nil
	case errors.Is(sweepErr, ErrBlocked):
		return workerResult(options, request, "blocked", "Harness cleanup was blocked by the final safety check; nothing was destroyed."), nil
	case errors.Is(sweepErr, ErrControlMismatch):
		return workerResult(options, request, "blocked", "Harness control record no longer matches the queued request; nothing was destroyed."), nil
	case errors.Is(sweepErr, ErrAlreadyGone):
		return workerResult(options, request, "already_gone", "Harness resources were already gone; no teardown was needed."), nil
	case errors.Is(sweepErr, ErrTeardown):
		return workerResult(options, request, "failed", "Harness teardown failed; inspect the action worker logs."), nil
	case errors.Is(sweepErr, ErrRetryable):
		return ActionResult{}, sweepErr
	case sweepErr != nil:
		result := workerResult(options, request, "failed", "Harness cleanup stopped on an unexpected worker error; automatic destructive retry was suppressed.")
		result.Evidence = map[string]string{"severity": "alarm", "error": sweepErr.Error()}
		return result, nil
	case candidate.State == StateResidue:
		result := workerResult(options, request, "partial", candidate.Reason)
		result.Evidence = map[string]string{"residueIndexLagCaveat": residueIndexLagCaveat}
		return result, nil
	case candidate.State == StateUnknown:
		message := candidate.SweepResult
		if message == "" {
			message = candidate.Reason
		}
		result := workerResult(options, request, "failed", message)
		result.Evidence = map[string]string{"severity": "alarm", "outcome": "inconclusive", "state": string(StateUnknown)}
		return result, nil
	case candidate.SweepResult == sweepResultDestroyed:
		return workerResult(options, request, "succeeded", "Harness teardown completed and no tagged resources remain."), nil
	default:
		return workerResult(options, request, "blocked", "Harness cleanup did not reach a destructive terminal outcome."), nil
	}
}

// ProcessReaperAction receives and processes at most one FIFO cleanup request.
func ProcessReaperAction(ctx context.Context, deps ReaperWorkerDeps, options ReaperWorkerOptions) (ActionResult, error) {
	options = options.defaults()
	if deps.IO == nil || deps.Sweeper == nil || deps.FinalVeto == nil {
		return ActionResult{}, fmt.Errorf("reaper worker IO, sweeper, and final veto are required")
	}
	message, err := deps.IO.ReceiveAction(ctx, options.ActionQueueURL, options.VisibilityTimeoutSeconds)
	if err != nil {
		return ActionResult{}, fmt.Errorf("receive action: %w", err)
	}
	if message == nil {
		return ActionResult{Status: "empty"}, nil
	}
	var request ReaperActionRequest
	if err := json.Unmarshal([]byte(message.Body), &request); err != nil {
		return ActionResult{}, deferActionForRetry(ctx, deps.IO, options, message, 30, fmt.Errorf("decode action request: %w", err))
	}
	if err := validateReaperAction(request, options.AccountID); err != nil {
		return ActionResult{}, deferActionForRetry(ctx, deps.IO, options, message, 30, err)
	}
	now := options.now().Unix()
	state, err := deps.IO.LoadControl(ctx, options.ControlTable, request, now)
	if err != nil {
		if errors.Is(err, ErrControlMismatch) {
			result := workerResult(options, request, "blocked", "Harness control record does not match the queued request; nothing was destroyed.")
			result.Evidence = map[string]string{"severity": "alarm", "error": err.Error()}
			return sendTerminalAndDelete(ctx, deps.IO, options, request, message, result)
		}
		return ActionResult{}, releaseActionForRetry(
			ctx, deps.IO, options, message,
			fmt.Errorf("load action control state: %w", err),
		)
	}
	if terminalActionStatuses[state.ActionStatus] {
		if err := deps.IO.DeleteAction(ctx, options.ActionQueueURL, message.ReceiptHandle); err != nil {
			return ActionResult{}, err
		}
		return workerResult(options, request, state.ActionStatus, "Duplicate delivery observed after the action reached a terminal state."), nil
	}
	if !state.ActionExists {
		if err := deps.IO.DeleteAction(ctx, options.ActionQueueURL, message.ReceiptHandle); err != nil {
			return ActionResult{}, err
		}
		return workerResult(options, request, "blocked", "Action record is missing; the queue message was discarded without teardown."), nil
	}
	if !activeControl(state, request, now) || state.HoldActive {
		result := workerResult(options, request, "blocked", "Harness action or active marker is missing, expired, mismatched, or held; nothing was destroyed.")
		return sendTerminalAndDelete(ctx, deps.IO, options, request, message, result)
	}
	if state.ActionStatus == "running" && !executionLeaseReady(state, request, now) {
		result := workerResult(options, request, "blocked", "Harness running-action lease is too short to cover the worker deadline; nothing was destroyed.")
		return sendTerminalAndDelete(ctx, deps.IO, options, request, message, result)
	}
	if state.ActionStatus == "queued" {
		running := workerResult(options, request, "running", "Harness action worker accepted the request and is revalidating the target.")
		if err := deps.IO.SendResult(ctx, options.ResultQueueURL, running); err != nil {
			return ActionResult{}, releaseActionForRetry(
				ctx, deps.IO, options, message,
				fmt.Errorf("send running result: %w", err),
			)
		}
		state, err = waitForRunning(ctx, deps.IO, options, request, running.OccurredAt)
		if err != nil {
			return ActionResult{}, releaseActionForRetry(ctx, deps.IO, options, message, err)
		}
		if state.ActionStatus == "cancelled" || state.HoldActive {
			status := "blocked"
			if state.ActionStatus == "cancelled" {
				status = "cancelled"
			}
			result := workerResult(options, request, status, "Harness action changed while the worker waited for running state; nothing was destroyed.")
			return sendTerminalAndDelete(ctx, deps.IO, options, request, message, result)
		}
	} else if state.ActionStatus != "running" {
		return ActionResult{}, releaseActionForRetry(
			ctx, deps.IO, options, message,
			fmt.Errorf("action status %q is not executable", state.ActionStatus),
		)
	}

	prefix, err := actionStatePrefix(request.Identity)
	if err != nil {
		return ActionResult{}, err
	}
	sweepContext, cancelSweep := context.WithCancel(ctx)
	defer cancelSweep()
	cancelHeartbeat, heartbeatFinished := startVisibilityHeartbeat(ctx, deps.IO, options, request, message, cancelSweep)
	prepared, ok := deps.Sweeper.(actionPreparedSweeper)
	if !ok {
		cancelHeartbeat()
		<-heartbeatFinished
		return ActionResult{}, releaseActionForRetry(
			ctx, deps.IO, options, message,
			fmt.Errorf("selected sweeper cannot install the final control-plane veto"),
		)
	}
	prepared.PrepareReaperAction(request, deps.FinalVeto)
	candidate, sweepErr := deps.Sweeper.SweepSelected(sweepContext, nil, prefix, request.ExpectedVersion)
	cancelHeartbeat()
	heartbeat := <-heartbeatFinished
	if heartbeat.result.Status == "visibility_lost" {
		if heartbeat.err != nil {
			return ActionResult{}, fmt.Errorf("send visibility_lost result: %w", heartbeat.err)
		}
		return heartbeat.result, nil
	}
	result, err := mapSweepResult(options, request, candidate, sweepErr)
	if err != nil {
		return ActionResult{}, releaseActionForRetry(ctx, deps.IO, options, message, err)
	}
	return sendTerminalAndDelete(ctx, deps.IO, options, request, message, result)
}

func createOnlyConflict(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.ErrorCode() == "PreconditionFailed" || apiError.ErrorCode() == "ConditionalRequestConflict"
}

// DrainCancelledReaperAction is the rollback-only queue drain. It archives and
// acknowledges one action only when the durable control record is already cancelled.
func DrainCancelledReaperAction(
	ctx context.Context,
	io ReaperWorkerIO,
	archive EngineReportWriter,
	archiveBucket string,
	options ReaperWorkerOptions,
) (ActionResult, error) {
	options = options.defaults()
	if io == nil || archive == nil || archiveBucket == "" {
		return ActionResult{}, fmt.Errorf("cancelled-action queue and archive dependencies are required")
	}
	message, err := io.ReceiveAction(ctx, options.ActionQueueURL, options.VisibilityTimeoutSeconds)
	if err != nil {
		return ActionResult{}, fmt.Errorf("receive cancelled action: %w", err)
	}
	if message == nil {
		return ActionResult{Status: "empty"}, nil
	}
	var request ReaperActionRequest
	if err := json.Unmarshal([]byte(message.Body), &request); err != nil {
		return ActionResult{}, fmt.Errorf("decode cancelled action: %w", err)
	}
	if err := validateReaperAction(request, options.AccountID); err != nil {
		return ActionResult{}, err
	}
	state, err := io.LoadControl(ctx, options.ControlTable, request, options.now().Unix())
	if err != nil {
		return ActionResult{}, err
	}
	if !state.ActionExists || state.ActionStatus != "cancelled" {
		return ActionResult{}, releaseActionForRetry(
			ctx, io, options, message,
			fmt.Errorf("action %s is not marked cancelled", request.ActionID),
		)
	}
	result := workerResult(options, request, "cancelled", "Cancelled action archived during Resource Reaper rollback; no teardown was attempted.")
	body, err := json.Marshal(struct {
		Request ReaperActionRequest `json:"request"`
		Result  ActionResult        `json:"result"`
	}{Request: request, Result: result})
	if err != nil {
		return ActionResult{}, err
	}
	key := "cancelled-actions/" + request.ActionID + ".json"
	_, err = archive.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(archiveBucket), Key: aws.String(key),
		Body: bytes.NewReader(body), ContentType: aws.String("application/json"),
		IfNoneMatch: aws.String("*"),
	})
	if err != nil && !createOnlyConflict(err) {
		return ActionResult{}, fmt.Errorf("archive cancelled action: %w", err)
	}
	return sendTerminalAndDelete(ctx, io, options, request, message, result)
}
