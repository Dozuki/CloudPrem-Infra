package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const usage = `usage: harness <provision|upgrade|validate|teardown|evidence|janitor> [flags]
  common: --run-id --config --repo-dir --account-id --profile --region --matrix --state-bucket [--mem-store]
  provision: --scenario <upgrade|fresh> --from-ref --to-ref --namespace
  teardown:  --keep-on-failure --failed
  evidence:  --region --max-nodes --timeout (reads a WorkflowList json on stdin,
             writes the CHILD_JSON array on stdout; needs neither --run-id nor --config)
  janitor:   --account-id --region --dr-region --self-workflow [--sweep] [--max-sweeps]
             [--max-sweep-failures] [--sweep-budget]
             (Phase 4 orphan sweeper; reads a WorkflowList json on stdin, defaults to a
             dry-run report; needs neither --run-id nor --config)`

func main() { os.Exit(dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]

	// evidence and janitor each have their own flags (no --run-id/--config) and branch
	// out before the shared flagset below demands either.
	if sub == "evidence" {
		return runEvidence(rest, stdin, stdout, stderr)
	}
	if sub == "janitor" {
		return runJanitor(rest, stdin, stdout, stderr)
	}

	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		runID    = fs.String("run-id", "", "base run id")
		cfgName  = fs.String("config", "", "matrix config name")
		repoDir  = fs.String("repo-dir", ".", "repo root")
		acct     = fs.String("account-id", "", "DDVtest account id")
		profile  = fs.String("profile", "", "AWS profile (empty in-cluster)")
		region   = fs.String("region", "us-east-1", "region")
		matrix   = fs.String("matrix", "live/tests/matrix.yaml", "matrix path")
		bucket   = fs.String("state-bucket", "", "harness state bucket")
		memStore = fs.Bool("mem-store", false, "use in-memory manifest store (dry-run/test)")
		scenario = fs.String("scenario", "upgrade", "provision: upgrade|fresh")
		fromRef  = fs.String("from-ref", "", "provision: baseline ref")
		toRef    = fs.String("to-ref", "", "provision: target ref")
		ns       = fs.String("namespace", "dozuki", "app namespace")
		keepFail = fs.Bool("keep-on-failure", false, "teardown: keep stack if failed")
		failed   = fs.Bool("failed", false, "teardown: mark run failed (full diagnostics)")
	)
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	switch sub {
	case "provision", "upgrade", "validate", "teardown":
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if *runID == "" || *cfgName == "" {
		fmt.Fprintln(stderr, "error: --run-id and --config are required\n"+usage)
		return 2
	}

	ctx := context.Background()
	m, err := harness.LoadMatrix(*matrix)
	if err != nil {
		fmt.Fprintf(stderr, "load matrix: %v\n", err)
		return 1
	}

	var store harness.ManifestStore
	if *memStore {
		store = harness.NewMemStore()
	} else {
		if *bucket == "" {
			fmt.Fprintln(stderr, "error: --state-bucket required (or pass --mem-store)")
			return 2
		}
		awsCfg, cerr := loadAWS(ctx, *profile, *region)
		if cerr != nil {
			fmt.Fprintf(stderr, "aws config: %v\n", cerr)
			return 1
		}
		store = harness.NewS3Store(s3.NewFromConfig(awsCfg), *bucket)
	}

	p := harness.PhaseParams{
		RepoDir: *repoDir, Matrix: m, Store: store, ConfigName: *cfgName,
		RunID: *runID, AccountID: *acct, Profile: *profile, Region: *region,
	}

	var perr error
	switch sub {
	case "provision":
		// deleteAfter computed here only if creating; persisted in manifest thereafter.
		ttl := m.Defaults.ReaperTTLHours
		if ttl == 0 {
			ttl = 24
		}
		da := deleteAfterFromTTL(ttl)
		fr, tr := resolveRefs(*repoDir, m, *fromRef, *toRef, *scenario)
		perr = p.Provision(ctx, *scenario, fr, tr, da, *ns)
	case "upgrade":
		perr = p.Upgrade(ctx)
	case "validate":
		perr = p.Validate(ctx)
	case "teardown":
		perr = p.Teardown(ctx, *keepFail, *failed)
	}
	if perr != nil {
		fmt.Fprintf(stderr, "%s failed: %v\n", sub, perr)
		return 1
	}
	return 0
}

// loadAWS loads AWS config for an optional shared-config profile + region.
func loadAWS(ctx context.Context, profile, region string) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	return config.LoadDefaultConfig(ctx, opts...)
}

// runEvidence reads a `kubectl get workflows -o json` WorkflowList from stdin and
// writes the CHILD_JSON array (the existing name/config/phase/msg/detail fields
// plus the new log_excerpt) to stdout.
//
// This can never silence the failure card: 20-matrix.yaml's post-status step falls
// back to its own jq expression on anything but a clean exit 0 with non-empty
// output, so any failure here just means the card reads today's node summary
// instead of the extracted error - never nothing.
func runEvidence(rest []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evidence", flag.ContinueOnError)
	fs.SetOutput(stderr)
	region := fs.String("region", "us-east-1", "AWS region for the S3 log fetch")
	maxNodes := fs.Int("max-nodes", 3, "max failed Pod nodes fetched per child")
	timeout := fs.Duration("timeout", 45*time.Second, "overall S3 fetch budget")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "evidence: read stdin: %v\n", err)
		return 1
	}

	var list harness.WorkflowList
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &list); err != nil {
			fmt.Fprintf(stderr, "evidence: parse workflows json: %v\n", err)
			return 1
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+10*time.Second)
	defer cancel()

	// Skip AWS entirely when there is nothing to fetch (no children, e.g. the
	// query raced the children existing yet) - keeps the common no-op case fast
	// and avoids depending on AWS creds being reachable at all.
	var fetcher harness.LogTail
	if len(list.Items) > 0 {
		awsCfg, err := loadAWS(ctx, "", *region)
		if err != nil {
			fmt.Fprintf(stderr, "evidence: aws config: %v\n", err)
			return 1
		}
		fetcher = harness.NewS3LogTail(s3.NewFromConfig(awsCfg))
	} else {
		fetcher = harness.NewS3LogTail(nil)
	}

	children := harness.Build(ctx, list, fetcher, harness.BuildOptions{
		MaxNodesPerChild: *maxNodes,
		OverallBudget:    *timeout,
	})

	out, err := json.Marshal(children)
	if err != nil {
		fmt.Fprintf(stderr, "evidence: marshal output: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(out))
	return 0
}

// runJanitor is the Phase 4 orphan sweeper. It reads a `kubectl get workflows -o json`
// WorkflowList from stdin (the same proven, already-RBAC'd pattern `harness evidence`
// uses - no client-go against the Argo cluster), recomputes each candidate's identity
// with the exact same code the apply used (Config.Salted), and only ever acts on a
// candidate that is simultaneously stale, unowned by any live workflow or fresh lock, and
// still holding real tagged AWS resources. Defaults to a dry-run report; --sweep and the
// harness-config `janitor_mode` ConfigMap key both have to say so before anything is
// destroyed.
//
// Exit codes: 2 is a usage error (bad flags). 3 is a SAFETY ABORT - the account identity
// did not match, the workflow list could not be trusted, or a candidate resolved to a
// protected identity - and means the janitor looked at nothing or stopped mid-cycle. 1
// means the cycle completed but a sweep destroy failed. 0 means the cycle completed,
// which is true even when the report lists orphans in report mode: the report is the
// signal, not the exit code, so a nightly report is not what pages anyone red.
func runJanitor(rest []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("janitor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		acct         = fs.String("account-id", "", "DDVtest account id (required; aborts if the caller identity does not match)")
		region       = fs.String("region", "us-east-1", "primary region")
		drRegion     = fs.String("dr-region", "us-west-2", "DR region")
		profile      = fs.String("profile", "", "AWS profile (empty in-cluster)")
		repoDir      = fs.String("repo-dir", ".", "repo root (only read when --sweep destroys something)")
		matrixPath   = fs.String("matrix", "live/tests/matrix.yaml", "matrix path")
		lockTable    = fs.String("lock-table", "dozuki-terraform-lock", "terraform state lock table name")
		grace        = fs.Duration("grace", 6*time.Hour, "extra staleness on top of the manifest's delete_after")
		lockFresh    = fs.Duration("lock-fresh", 4*time.Hour, "a lock younger than this means something is applying right now")
		selfWorkflow = fs.String("self-workflow", "", "this janitor workflow's own name (required; must appear in the stdin workflow list)")
		sweep        = fs.Bool("sweep", false, "actually destroy orphans; default is dry-run report-only")
		maxSweeps    = fs.Int("max-sweeps", 1, "cap on successful destroys performed in one cycle")
		maxSweepFail = fs.Int("max-sweep-failures", 2, "cap on FAILED destroy attempts in one cycle, independent of --max-sweeps (bounds total attempts against the pod deadline; see janitor.go Sweep)")
		sweepBudget  = fs.Duration("sweep-budget", 0, "wall-clock budget for starting new Sweep destroy attempts in one cycle; <= 0 derives it from harness.JanitorPodActiveDeadlineSeconds (see janitor.go DefaultSweepBudget) rather than a second independent number")
		jsonOut      = fs.Bool("json", false, "also print the report as JSON on the last stdout line")
	)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *acct == "" {
		fmt.Fprintln(stderr, "error: --account-id is required\n"+usage)
		return 2
	}
	if *selfWorkflow == "" {
		fmt.Fprintln(stderr, "error: --self-workflow is required — the ownership check must be able to prove it is looking at a real workflow list\n"+usage)
		return 2
	}

	// G2: the ownership check is only as good as this list. Empty stdin (the caller's
	// `kubectl ... || : > file` fallback on a kubectl failure), unparseable JSON, or a
	// list that does not even contain the janitor's OWN workflow all mean the signal is
	// blind. A janitor that cannot see what is running must do nothing, not assume
	// nothing is running - so every one of these aborts before a single candidate is
	// evaluated.
	body, rerr := io.ReadAll(stdin)
	if rerr != nil {
		fmt.Fprintf(stderr, "janitor: read stdin: %v\n", rerr)
		return 3
	}
	var wfList harness.JanitorWorkflowList
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &wfList); err != nil {
			fmt.Fprintf(stderr, "janitor: parse workflows json: %v — the ownership check is blind; aborting\n", err)
			return 3
		}
	}
	selfSeen := false
	for _, w := range wfList.Items {
		if w.Metadata.Name == *selfWorkflow {
			selfSeen = true
			break
		}
	}
	if !selfSeen {
		fmt.Fprintf(stderr, "janitor: --self-workflow %q not found among %d workflows on stdin — the ownership check is blind; aborting\n", *selfWorkflow, len(wfList.Items))
		return 3
	}

	ctx := context.Background()
	awsCfg, cerr := loadAWS(ctx, *profile, *region)
	if cerr != nil {
		fmt.Fprintf(stderr, "janitor: aws config (%s): %v\n", *region, cerr)
		return 1
	}
	drCfg, dcerr := loadAWS(ctx, *profile, *drRegion)
	if dcerr != nil {
		fmt.Fprintf(stderr, "janitor: aws config (%s): %v\n", *drRegion, dcerr)
		return 1
	}

	// G1: the account identity must match --account-id before anything is listed. A
	// mismatch here means the profile chain resolved to the wrong account entirely, and
	// nothing downstream can be trusted.
	ident, iderr := sts.NewFromConfig(awsCfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if iderr != nil {
		fmt.Fprintf(stderr, "janitor: sts get-caller-identity: %v\n", iderr)
		return 1
	}
	if aws.ToString(ident.Account) != *acct {
		fmt.Fprintf(stderr, "janitor: caller identity account %q does not match --account-id %q — refusing to scan the wrong account\n", aws.ToString(ident.Account), *acct)
		return 3
	}

	m, merr := harness.LoadMatrix(*matrixPath)
	if merr != nil {
		fmt.Fprintf(stderr, "janitor: load matrix: %v\n", merr)
		return 1
	}

	s3Router := multiRegionS3{byRegion: map[string]*s3.Client{
		*region:   s3.NewFromConfig(awsCfg),
		*drRegion: s3.NewFromConfig(drCfg),
	}}
	deps := harness.JanitorDeps{
		S3: s3Router,
		Tags: map[string]harness.TagAPI{
			*region:   resourcegroupstaggingapi.NewFromConfig(awsCfg),
			*drRegion: resourcegroupstaggingapi.NewFromConfig(drCfg),
		},
		Locks:    dynamodb.NewFromConfig(awsCfg),
		Matrix:   m,
		Teardown: harness.RealTeardown,
	}
	opts := harness.JanitorOptions{
		AccountID: *acct, Region: *region, DRRegion: *drRegion, Profile: *profile,
		RepoDir: *repoDir, LockTable: *lockTable, Grace: *grace, LockFresh: *lockFresh,
		SelfWorkflow: *selfWorkflow, Sweep: *sweep, MaxSweeps: *maxSweeps,
		MaxSweepFailures: *maxSweepFail, SweepBudget: *sweepBudget,
	}

	rep, serr := harness.Scan(ctx, deps, opts, wfList)
	if serr != nil {
		// G3 lands here too (guardProtected returns ErrProtected up through Scan): a
		// protected identity reaching the candidate path means the detection logic is
		// wrong, and the whole cycle aborts rather than skip just that one candidate.
		fmt.Fprintf(stderr, "janitor: scan aborted: %v\n", serr)
		return 3
	}
	printJanitorReport(stdout, rep)

	exitCode := 0
	if *sweep {
		if err := harness.Sweep(ctx, deps, opts, rep); err != nil {
			fmt.Fprintf(stderr, "janitor: sweep aborted: %v\n", err)
			return 3
		}
		fmt.Fprintln(stdout)
		printJanitorReport(stdout, rep)
		if rep.Failed > 0 {
			exitCode = 1
		}
	}

	if *jsonOut {
		out, jerr := json.Marshal(rep)
		if jerr != nil {
			fmt.Fprintf(stderr, "janitor: marshal report: %v\n", jerr)
			return 1
		}
		fmt.Fprintln(stdout, string(out))
	}
	return exitCode
}

// printJanitorReport renders the human-readable table the pod log and the "before" half
// of a sweep run show. The JSON report (--json) is a separate, later line, so a reader
// tailing the pod log sees this first regardless of --json.
func printJanitorReport(w io.Writer, rep *harness.Report) {
	fmt.Fprintf(w, "\n== harness janitor report (%s, %s) ==\n", rep.Mode, rep.At)
	if len(rep.Candidates) == 0 {
		fmt.Fprintln(w, "no candidate run prefixes found")
		return
	}
	for _, c := range rep.Candidates {
		line := fmt.Sprintf("[%s] %s (config=%s identifier=%s resources=%d)", c.State, c.Prefix, c.ConfigName, c.Identifier, c.Resources)
		if c.SweepResult != "" {
			line += " sweep=" + c.SweepResult
		}
		fmt.Fprintln(w, line)
		fmt.Fprintln(w, "    "+c.Reason)
	}
	fmt.Fprintf(w, "-- orphans=%d swept=%d failed=%d residue=%d --\n", rep.Orphans, rep.Swept, rep.Failed, rep.Residue)
}

// multiRegionS3 routes each call to the client whose signing region matches the target
// bucket, derived from the bucket name (phases.go stateBucket: "...-state-<region>-
// <account>"). A single client cannot address both buckets: SigV4 requires the signing
// region to match where the bucket actually lives (us-east-1 is the one legacy
// exception), and the janitor's two-bucket scan (primary + DR) needs both.
type multiRegionS3 struct {
	byRegion map[string]*s3.Client
}

func (m multiRegionS3) clientFor(bucket string) *s3.Client {
	for region, c := range m.byRegion {
		if strings.Contains(bucket, "-"+region+"-") {
			return c
		}
	}
	// An unrecognized bucket name falls through to whichever client is configured, so the
	// resulting AWS error names the real problem instead of a nil-pointer panic.
	for _, c := range m.byRegion {
		return c
	}
	return nil
}

func (m multiRegionS3) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return m.clientFor(aws.ToString(in.Bucket)).GetObject(ctx, in, opts...)
}

func (m multiRegionS3) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return m.clientFor(aws.ToString(in.Bucket)).PutObject(ctx, in, opts...)
}

func (m multiRegionS3) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return m.clientFor(aws.ToString(in.Bucket)).ListObjectsV2(ctx, in, opts...)
}
