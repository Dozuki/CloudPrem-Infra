package harness

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMarkerName_flattensStatePrefix(t *testing.T) {
	got := markerName("local-1-min_default-min_default/")
	want := "local-1-min_default-min_default"
	if got != want {
		t.Fatalf("markerName = %q, want %q", got, want)
	}
	if got := markerName("a/b/c/"); got != "a_b_c" {
		t.Fatalf("markerName nested = %q, want a_b_c", got)
	}
}

func TestAppliedMarker_writeReadRemove(t *testing.T) {
	repo := t.TempDir()
	envDir := filepath.Join(repo, "live", "tests", "__worktrees__", "run1", "v5.3", "live", "standard", "us-east-1", "min")
	const sp = "local-1-min_default-min_default/"

	if _, err := readAppliedMarker(repo, sp); err == nil {
		t.Fatal("expected error reading a missing marker")
	}
	if err := writeAppliedMarker(repo, sp, envDir); err != nil {
		t.Fatalf("writeAppliedMarker: %v", err)
	}
	got, err := readAppliedMarker(repo, sp)
	if err != nil {
		t.Fatalf("readAppliedMarker: %v", err)
	}
	if got != envDir {
		t.Fatalf("readAppliedMarker = %q, want %q", got, envDir)
	}
	removeAppliedMarker(repo, sp)
	if _, err := readAppliedMarker(repo, sp); err == nil {
		t.Fatal("expected error after remove")
	}
}

func TestWatchSignals_runsTeardownThenExitOnSignal(t *testing.T) {
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	var torn, exited int
	teardown := func() { torn++ }
	exit := func(int) { exited++ }

	sigCh <- syscall.SIGTERM // signal already pending
	watchSignals(sigCh, done, teardown, exit)

	if torn != 1 || exited != 1 {
		t.Fatalf("on signal: torn=%d exited=%d, want 1/1", torn, exited)
	}
}

func TestWatchSignals_noTeardownWhenDoneClosed(t *testing.T) {
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	var torn int
	close(done) // normal-exit path
	watchSignals(sigCh, done, func() { torn++ }, func(int) {})
	if torn != 0 {
		t.Fatalf("on done: torn=%d, want 0", torn)
	}
}
