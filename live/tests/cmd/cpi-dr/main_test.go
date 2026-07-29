package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUsageAndRequiredFlags(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run(nil, &out, &errb); rc != 2 {
		t.Errorf("no args: rc=%d, want 2", rc)
	}
	errb.Reset()
	// prepare without its flags must refuse before touching AWS.
	if rc := run([]string{"prepare"}, &out, &errb); rc != 2 {
		t.Errorf("bare prepare: rc=%d, want 2", rc)
	}
	if !strings.Contains(errb.String(), "--dr-region is required") {
		t.Errorf("missing-flag message absent: %s", errb.String())
	}
	errb.Reset()
	// rebuild validates every required flag the scaffold needs.
	if rc := run([]string{"rebuild", "--dr-region", "us-west-2"}, &out, &errb); rc != 2 {
		t.Errorf("partial rebuild: rc=%d, want 2", rc)
	}
	errb.Reset()
	if rc := run([]string{"nonsense"}, &out, &errb); rc != 2 {
		t.Errorf("unknown subcommand: rc=%d, want 2", rc)
	}
}

func TestBucketFlags(t *testing.T) {
	var b bucketFlags
	for _, kv := range []string{"image=x-image-dr", "obj=x-obj-dr"} {
		if err := b.Set(kv); err != nil {
			t.Fatal(err)
		}
	}
	if b.m["image"] != "x-image-dr" || b.String() != "image=x-image-dr,obj=x-obj-dr" {
		t.Errorf("bucketFlags wrong: %q", b.String())
	}
	if err := b.Set("garbage"); err == nil {
		t.Error("malformed --bucket accepted")
	}
}
