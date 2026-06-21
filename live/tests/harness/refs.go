package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func NewestTags(repoDir string) ([]string, error) {
	cmd := exec.Command("git", "tag", "--sort=-creatordate")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git tag: %w", err)
	}
	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

func ResolveRef(ref string, newestFirstTags []string) (string, error) {
	switch ref {
	case "auto:latest":
		if len(newestFirstTags) < 1 {
			return "", fmt.Errorf("no tags available for auto:latest")
		}
		return newestFirstTags[0], nil
	case "auto:latest-1":
		if len(newestFirstTags) < 2 {
			return "", fmt.Errorf("need >=2 tags for auto:latest-1, have %d", len(newestFirstTags))
		}
		return newestFirstTags[1], nil
	default:
		return ref, nil
	}
}

type Worktree struct {
	Dir string
	Ref string
}

// FetchOrigin updates remote-tracking refs so branch refs resolve to their pushed
// state instead of a possibly-stale local branch. Non-fatal: an offline run
// continues with whatever refs are already local.
func FetchOrigin(repoDir string) {
	// Use HTTPS (via the gh credential helper) even when origin is an SSH remote:
	// SSH to github.com is blocked / times out in some local and CI environments,
	// the same reason AddWorktree rewrites submodule URLs to HTTPS below. A hard
	// timeout keeps a slow or blocked network from hanging the start of every run —
	// stale refs are a warning, not a reason to stall.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git",
		"-c", "url.https://github.com/.insteadOf=git@github.com:",
		"fetch", "origin", "--tags", "--prune")
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, ">> warning: git fetch origin failed (refs may be stale): %v\n", err)
	}
}

func AddWorktree(repoDir, baseDir, ref string, initSubmodules bool) (*Worktree, error) {
	dir := filepath.Join(baseDir, sanitize(ref))
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	// Prefer the remote-tracking branch so the harness always tests the PUSHED state.
	// A release branch (e.g. v6.1-release) that moved on origin after being cut would
	// otherwise be silently tested from a stale local branch — exactly the failure
	// that let a pre-fix v6.1-release run against outdated DR-provider code. Tags and
	// SHAs have no origin/<ref> and fall through to the ref as given.
	checkout := ref
	if hasRemoteBranch(repoDir, ref) {
		checkout = "origin/" + ref
	}
	if err := run(repoDir, "git", "worktree", "add", "--detach", dir, checkout); err != nil {
		return nil, fmt.Errorf("worktree add %s (%s): %w", ref, checkout, err)
	}
	if initSubmodules {
		// Baseline refs older than helm#143 pin the chart submodule via an SSH URL
		// (git@github.com:Dozuki/helm.git). Rewrite to HTTPS for the clone so it
		// uses the gh credential helper — SSH submodule clones fail in CI and some
		// local contexts ("Repository not found") even when the repo is accessible.
		if err := run(dir, "git", "-c", "url.https://github.com/.insteadOf=git@github.com:",
			"submodule", "update", "--init", "--recursive"); err != nil {
			return nil, fmt.Errorf("submodule init in %s: %w", ref, err)
		}
	}
	return &Worktree{Dir: dir, Ref: ref}, nil
}

func (w *Worktree) Remove(repoDir string) error {
	return run(repoDir, "git", "worktree", "remove", "--force", w.Dir)
}

func (w *Worktree) HasSubmodule() bool {
	_, err := os.Stat(filepath.Join(w.Dir, ".gitmodules"))
	return err == nil
}

// hasRemoteBranch reports whether refs/remotes/origin/<ref> exists (i.e. ref names
// a remote branch, not a tag or raw SHA).
func hasRemoteBranch(repoDir, ref string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+ref)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func sanitize(ref string) string {
	return strings.NewReplacer("/", "_", ":", "_", " ", "_", "..", "_").Replace(ref)
}

func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
