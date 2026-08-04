package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const usage = `usage: harness <provision|upgrade|validate|teardown|evidence> [flags]
  common: --run-id --config --repo-dir --account-id --profile --region --matrix --state-bucket [--mem-store]
  provision: --scenario <upgrade|fresh> --from-ref --to-ref --namespace
  teardown:  --keep-on-failure --failed
  evidence:  --region --max-nodes --timeout (reads a WorkflowList json on stdin,
             writes the CHILD_JSON array on stdout; needs neither --run-id nor --config)`

func main() { os.Exit(dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]

	// evidence has its own flags (no --run-id/--config) and no matrix, so it
	// branches out before the shared flagset below demands either.
	if sub == "evidence" {
		return runEvidence(rest, stdin, stdout, stderr)
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
