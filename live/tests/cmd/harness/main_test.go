package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestDispatchUnknownSubcommand(t *testing.T) {
	var b bytes.Buffer
	if code := dispatch([]string{"frobnicate"}, strings.NewReader(""), io.Discard, &b); code == 0 {
		t.Fatalf("unknown subcommand should be non-zero")
	}
	if !strings.Contains(b.String(), "usage") {
		t.Fatalf("expected usage text, got %q", b.String())
	}
}

func TestDispatchProvisionRequiresFlags(t *testing.T) {
	var b bytes.Buffer
	// missing --run-id/--config → non-zero with a clear message
	if code := dispatch([]string{"provision", "--scenario", "fresh"}, strings.NewReader(""), io.Discard, &b); code == 0 {
		t.Fatalf("missing required flags should fail")
	}
}

func TestDispatchEvidenceDoesNotRequireRunFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := dispatch([]string{"evidence"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("evidence with empty stdin: code = %d, stderr = %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "[]" {
		t.Fatalf("stdout = %q, want []", got)
	}
}

func TestDispatchEvidenceRejectsMalformedInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := dispatch([]string{"evidence"}, strings.NewReader("not json"), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("malformed stdin should be non-zero (so the caller's jq fallback triggers)")
	}
}

// ---- janitor bucket routing ----

// TestMultiRegionS3RoutesByExactBucketName: the router used to pick a client by
// substring-matching a region name inside the bucket name, which mismatches the moment
// one configured region's name is a prefix of another's. The table is now built from
// harness.StateBucket - the same function that composes the names the janitor asks for -
// so matching is exact and an unrecognized bucket falls back to ONE named client rather
// than whatever a randomized map range hands back first.
func TestMultiRegionS3RoutesByExactBucketName(t *testing.T) {
	const acct = "076248559428"
	primary := s3.New(s3.Options{Region: "us-east-1"})
	dr := s3.New(s3.Options{Region: "us-west-2"})
	// "us-west-2" is deliberately alongside a region whose name it contains, the shape
	// the old substring match got wrong.
	extra := s3.New(s3.Options{Region: "us-west-2-lax-1"})

	r, err := newMultiRegionS3(acct, "us-east-1", map[string]*s3.Client{
		"us-east-1":       primary,
		"us-west-2":       dr,
		"us-west-2-lax-1": extra,
	})
	if err != nil {
		t.Fatalf("newMultiRegionS3: %v", err)
	}

	cases := []struct {
		bucket string
		want   *s3.Client
		why    string
	}{
		{harness.StateBucket(acct, "us-east-1"), primary, "the primary state bucket"},
		{harness.StateBucket(acct, "us-west-2"), dr, "the DR state bucket"},
		{harness.StateBucket(acct, "us-west-2-lax-1"), extra, "a bucket whose region name CONTAINS another configured region"},
		{"some-unrelated-bucket", primary, "an unrecognized bucket falls back to the primary client, deterministically"},
	}
	for _, c := range cases {
		got, gerr := r.clientFor(c.bucket)
		if gerr != nil {
			t.Errorf("clientFor(%q): %v", c.bucket, gerr)
			continue
		}
		if got != c.want {
			t.Errorf("clientFor(%q) routed to the wrong client (%s)", c.bucket, c.why)
		}
	}
}

// TestMultiRegionS3RequiresAPrimaryClient: a router with no fallback would fail every
// unrecognized bucket deep inside a scan. A configuration error belongs at startup.
func TestMultiRegionS3RequiresAPrimaryClient(t *testing.T) {
	if _, err := newMultiRegionS3("076248559428", "us-east-1", map[string]*s3.Client{
		"us-west-2": s3.New(s3.Options{Region: "us-west-2"}),
	}); err == nil {
		t.Fatal("newMultiRegionS3 accepted a client set with no primary-region client")
	}
	// And the zero value never hands back a nil client for a caller to dereference.
	var zero multiRegionS3
	if _, err := zero.clientFor("anything"); err == nil {
		t.Fatal("the zero-value router returned a client")
	}
}

// TestJanitorExitCode: a post-destroy verification failure must reach the exit code.
// The Argo verify-scan gate keys off the scan step's status, so an outcome that exits 0
// is an outcome nobody is ever told about twice.
func TestJanitorExitCode(t *testing.T) {
	cases := []struct {
		name string
		rep  *harness.Report
		want int
	}{
		{"nil report", nil, 0},
		{"clean sweep", &harness.Report{Swept: 2}, 0},
		{"a destroy failed", &harness.Report{Failed: 1}, 1},
		{"a post-destroy verification failed", &harness.Report{Swept: 1, Inconclusive: 1}, 1},
		{"residue alone stays green", &harness.Report{Residue: 3}, 0},
	}
	for _, c := range cases {
		if got := janitorExitCode(c.rep); got != c.want {
			t.Errorf("%s: janitorExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}
