package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispatchUnknownSubcommand(t *testing.T) {
	var b bytes.Buffer
	if code := dispatch([]string{"frobnicate"}, &b); code == 0 {
		t.Fatalf("unknown subcommand should be non-zero")
	}
	if !strings.Contains(b.String(), "usage") {
		t.Fatalf("expected usage text, got %q", b.String())
	}
}

func TestDispatchProvisionRequiresFlags(t *testing.T) {
	var b bytes.Buffer
	// missing --run-id/--config → non-zero with a clear message
	if code := dispatch([]string{"provision", "--scenario", "fresh"}, &b); code == 0 {
		t.Fatalf("missing required flags should fail")
	}
}
