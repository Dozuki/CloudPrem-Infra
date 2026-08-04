package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
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
