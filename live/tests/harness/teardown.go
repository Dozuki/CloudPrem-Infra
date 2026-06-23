package harness

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// markerDir holds per-run applied-ref markers, outside any single run's worktree
// dir so it survives worktree removal.
func markerDir(repoDir string) string {
	return filepath.Join(repoDir, "live", "tests", "__worktrees__", ".markers")
}

// markerName flattens a state prefix (which ends in "/") into a flat filename.
func markerName(statePrefix string) string {
	return strings.ReplaceAll(strings.TrimSuffix(statePrefix, "/"), "/", "_")
}

// writeAppliedMarker records the env dir of the currently-applied worktree, keyed
// by state prefix, so the out-of-process cleanup backstop can destroy against the
// matching ref's code instead of the live tree.
func writeAppliedMarker(repoDir, statePrefix, envDir string) error {
	d := markerDir(repoDir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, markerName(statePrefix)), []byte(envDir+"\n"), 0o644)
}

// readAppliedMarker returns the recorded env dir for a state prefix.
func readAppliedMarker(repoDir, statePrefix string) (string, error) {
	b, err := os.ReadFile(filepath.Join(markerDir(repoDir), markerName(statePrefix)))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// removeAppliedMarker deletes a run's marker (best-effort).
func removeAppliedMarker(repoDir, statePrefix string) {
	_ = os.Remove(filepath.Join(markerDir(repoDir), markerName(statePrefix)))
}

// watchSignals runs teardown() once on a signal, then exit(1). Returns when either
// a signal arrives (after teardown+exit) or done is closed. Split from signal.Notify
// wiring so it is unit-testable with a fake channel.
func watchSignals(sigCh <-chan os.Signal, done <-chan struct{}, teardown func(), exit func(int)) {
	select {
	case <-sigCh:
		fmt.Fprintf(os.Stderr, "\n>> [harness] interrupted — running teardown before exit\n")
		teardown()
		exit(1)
	case <-done:
	}
}

// installTeardownOnSignal wires SIGINT/SIGTERM to watchSignals. The returned stop()
// unregisters the handler and ends the goroutine; call it via defer.
func installTeardownOnSignal(teardown func(), exit func(int)) (stop func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go watchSignals(sigCh, done, teardown, exit)
	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}
