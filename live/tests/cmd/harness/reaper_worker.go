package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type reaperWorkerFlags struct {
	actionQueueURL string
	resultQueueURL string
	controlTable   string
	accountID      string
	region         string
	drRegion       string
	profile        string
	repoDir        string
	matrixPath     string
	lockTable      string
	selfWorkflow   string
	actionsEnabled bool
}

func parseReaperWorkerFlags(rest []string, stderr io.Writer) (reaperWorkerFlags, int) {
	fs := flag.NewFlagSet("reaper-worker", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var flags reaperWorkerFlags
	fs.StringVar(&flags.actionQueueURL, "action-queue-url", "", "Resource Reaper harness action FIFO URL")
	fs.StringVar(&flags.resultQueueURL, "result-queue-url", "", "Resource Reaper harness result FIFO URL")
	fs.StringVar(&flags.controlTable, "control-table", "", "Resource Reaper control table name")
	fs.StringVar(&flags.accountID, "account-id", "", "DDVtest account id")
	fs.StringVar(&flags.region, "region", "us-east-1", "primary region")
	fs.StringVar(&flags.drRegion, "dr-region", "us-west-2", "DR region")
	fs.StringVar(&flags.profile, "profile", "", "AWS profile (empty in-cluster)")
	fs.StringVar(&flags.repoDir, "repo-dir", ".", "repo root used by teardown")
	fs.StringVar(&flags.matrixPath, "matrix", "live/tests/matrix.yaml", "harness matrix path")
	fs.StringVar(&flags.lockTable, "lock-table", "dozuki-terraform-lock", "terraform state lock table")
	fs.StringVar(&flags.selfWorkflow, "self-workflow", "", "this worker workflow name")
	fs.BoolVar(&flags.actionsEnabled, "actions-enabled", false, "permit one Resource Reaper queue receive")
	if err := fs.Parse(rest); err != nil {
		return flags, 2
	}
	if flags.actionQueueURL == "" || flags.resultQueueURL == "" || flags.controlTable == "" || flags.accountID == "" || flags.selfWorkflow == "" {
		fmt.Fprintln(stderr, "error: --action-queue-url, --result-queue-url, --control-table, --account-id, and --self-workflow are required")
		return flags, 2
	}
	return flags, 0
}

func readWorkerWorkflows(stdin io.Reader, selfWorkflow string) (harness.JanitorWorkflowList, error) {
	body, err := io.ReadAll(stdin)
	if err != nil {
		return harness.JanitorWorkflowList{}, err
	}
	var workflows harness.JanitorWorkflowList
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &workflows); err != nil {
			return workflows, fmt.Errorf("parse workflows json: %w", err)
		}
	}
	for _, workflow := range workflows.Items {
		if workflow.Metadata.Name == selfWorkflow {
			return workflows, nil
		}
	}
	return workflows, fmt.Errorf("--self-workflow %q not found among %d workflows", selfWorkflow, len(workflows.Items))
}

func runReaperWorker(rest []string, stdin io.Reader, stdout, stderr io.Writer) int {
	processStart := time.Now()
	flags, code := parseReaperWorkerFlags(rest, stderr)
	if code != 0 {
		return code
	}
	if !flags.actionsEnabled {
		fmt.Fprintln(stdout, `{"status":"disabled"}`)
		return 0
	}
	workflows, err := readWorkerWorkflows(stdin, flags.selfWorkflow)
	if err != nil {
		fmt.Fprintf(stderr, "reaper-worker: %v — the ownership check is blind; aborting\n", err)
		return 3
	}
	ctx := context.Background()
	awsConfig, err := loadAWS(ctx, flags.profile, flags.region)
	if err != nil {
		fmt.Fprintf(stderr, "reaper-worker: aws config (%s): %v\n", flags.region, err)
		return 1
	}
	drConfig, err := loadAWS(ctx, flags.profile, flags.drRegion)
	if err != nil {
		fmt.Fprintf(stderr, "reaper-worker: aws config (%s): %v\n", flags.drRegion, err)
		return 1
	}
	identity, err := sts.NewFromConfig(awsConfig).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		fmt.Fprintf(stderr, "reaper-worker: sts get-caller-identity: %v\n", err)
		return 3
	}
	if aws.ToString(identity.Account) != flags.accountID {
		fmt.Fprintf(stderr, "reaper-worker: caller identity account %q does not match --account-id %q\n", aws.ToString(identity.Account), flags.accountID)
		return 3
	}
	matrix, err := harness.LoadMatrix(flags.matrixPath)
	if err != nil {
		fmt.Fprintf(stderr, "reaper-worker: load matrix: %v\n", err)
		return 1
	}
	s3Router, err := newMultiRegionS3(flags.accountID, flags.region, map[string]*s3.Client{
		flags.region: s3.NewFromConfig(awsConfig), flags.drRegion: s3.NewFromConfig(drConfig),
	})
	if err != nil {
		fmt.Fprintf(stderr, "reaper-worker: %v\n", err)
		return 1
	}
	janitorDeps := harness.JanitorDeps{
		S3: s3Router,
		Tags: map[string]harness.TagAPI{
			flags.region: resourcegroupstaggingapi.NewFromConfig(awsConfig), flags.drRegion: resourcegroupstaggingapi.NewFromConfig(drConfig),
		},
		Locks: map[string]harness.LockAPI{
			flags.region: dynamodb.NewFromConfig(awsConfig), flags.drRegion: dynamodb.NewFromConfig(drConfig),
		},
		Digests: map[string]harness.DigestAPI{
			flags.region: dynamodb.NewFromConfig(awsConfig), flags.drRegion: dynamodb.NewFromConfig(drConfig),
		},
		DMS: map[string]harness.DMSReclaimAPI{
			flags.region: databasemigrationservice.NewFromConfig(awsConfig), flags.drRegion: databasemigrationservice.NewFromConfig(drConfig),
		},
		Matrix: matrix, Teardown: harness.RealTeardown,
	}
	janitorOptions := harness.JanitorOptions{
		AccountID: flags.accountID, Region: flags.region, DRRegion: flags.drRegion,
		Profile: flags.profile, RepoDir: flags.repoDir, LockTable: flags.lockTable,
		Grace: 6 * time.Hour, LockFresh: 4 * time.Hour, SelfWorkflow: flags.selfWorkflow,
		Sweep: true, MaxSweeps: 1, MaxSweepFailures: 1, ProcessStart: processStart,
	}
	workerIO := &harness.AWSReaperWorkerIO{
		Queue: sqs.NewFromConfig(awsConfig), Control: dynamodb.NewFromConfig(awsConfig),
	}
	janitor := &harness.Janitor{Deps: janitorDeps, Options: janitorOptions, Workflows: workflows}
	workerOptions := harness.ReaperWorkerOptions{
		ActionQueueURL: flags.actionQueueURL, ResultQueueURL: flags.resultQueueURL,
		ControlTable: flags.controlTable, AccountID: flags.accountID,
		VisibilityTimeoutSeconds: 13500,
	}
	finalVeto := func(ctx context.Context, request harness.ReaperActionRequest, _ harness.Candidate) error {
		now := time.Now().UTC().Unix()
		state, err := workerIO.LoadControl(ctx, flags.controlTable, request, now)
		if err != nil {
			if errors.Is(err, harness.ErrControlMismatch) {
				return err
			}
			return fmt.Errorf("%w: final control-plane read: %v", harness.ErrRetryable, err)
		}
		if !state.ActionExists || state.ActionStatus != "running" || state.ActiveActionID != request.ActionID || state.ActiveExpiresAt < now+int64(harness.JanitorPodActiveDeadlineSeconds) || state.HoldActive {
			return harness.ErrBlocked
		}
		return nil
	}
	result, err := harness.ProcessReaperAction(
		ctx,
		harness.ReaperWorkerDeps{IO: workerIO, Sweeper: janitor, FinalVeto: finalVeto},
		workerOptions,
	)
	if err != nil {
		fmt.Fprintf(stderr, "reaper-worker: %v\n", err)
		return 1
	}
	encoded, _ := json.Marshal(result)
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

func runReaperDrainCancelled(rest []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reaper-drain-cancelled", flag.ContinueOnError)
	fs.SetOutput(stderr)
	actionQueueURL := fs.String("action-queue-url", "", "Resource Reaper harness action FIFO URL")
	resultQueueURL := fs.String("result-queue-url", "", "Resource Reaper harness result FIFO URL")
	controlTable := fs.String("control-table", "", "Resource Reaper control table name")
	archiveBucket := fs.String("archive-bucket", "", "Resource Reaper archive bucket")
	accountID := fs.String("account-id", "", "DDVtest account id")
	region := fs.String("region", "us-east-1", "Resource Reaper region")
	profile := fs.String("profile", "", "AWS profile (empty in-cluster)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *actionQueueURL == "" || *resultQueueURL == "" || *controlTable == "" || *archiveBucket == "" || *accountID == "" {
		fmt.Fprintln(stderr, "error: --action-queue-url, --result-queue-url, --control-table, --archive-bucket, and --account-id are required")
		return 2
	}
	ctx := context.Background()
	awsConfig, err := loadAWS(ctx, *profile, *region)
	if err != nil {
		fmt.Fprintf(stderr, "reaper-drain-cancelled: aws config: %v\n", err)
		return 1
	}
	identity, err := sts.NewFromConfig(awsConfig).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		fmt.Fprintf(stderr, "reaper-drain-cancelled: sts get-caller-identity: %v\n", err)
		return 3
	}
	if aws.ToString(identity.Account) != *accountID {
		fmt.Fprintf(stderr, "reaper-drain-cancelled: caller identity account %q does not match --account-id %q\n", aws.ToString(identity.Account), *accountID)
		return 3
	}
	workerIO := &harness.AWSReaperWorkerIO{
		Queue: sqs.NewFromConfig(awsConfig), Control: dynamodb.NewFromConfig(awsConfig),
	}
	options := harness.ReaperWorkerOptions{
		ActionQueueURL: *actionQueueURL, ResultQueueURL: *resultQueueURL,
		ControlTable: *controlTable, AccountID: *accountID,
		VisibilityTimeoutSeconds: 30,
	}
	for count := 0; count < 1000; count++ {
		result, err := harness.DrainCancelledReaperAction(
			ctx, workerIO, s3.NewFromConfig(awsConfig), *archiveBucket, options,
		)
		if err != nil {
			fmt.Fprintf(stderr, "reaper-drain-cancelled: %v\n", err)
			return 1
		}
		if result.Status == "empty" {
			return 0
		}
		encoded, _ := json.Marshal(result)
		fmt.Fprintln(stdout, string(encoded))
	}
	fmt.Fprintln(stderr, "reaper-drain-cancelled: safety cap reached after 1000 actions")
	return 1
}
