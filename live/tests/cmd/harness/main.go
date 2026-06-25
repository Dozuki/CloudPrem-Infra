package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const usage = `usage: harness <provision|upgrade|validate|teardown> [flags]
  common: --run-id --config --repo-dir --account-id --profile --region --matrix --state-bucket [--mem-store]
  provision: --scenario <upgrade|fresh> --from-ref --to-ref --namespace
  teardown:  --keep-on-failure --failed`

func main() { os.Exit(dispatch(os.Args[1:], os.Stderr)) }

func dispatch(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
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
