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
	} else if !resolvesToCommit(repoDir, checkout) {
		// A PR-head SHA stops arriving via FetchOrigin the moment the PR merges:
		// GitHub deletes the branch, and refs/pull/*/head (where the commit stays
		// reachable server-side) is never part of a normal fetch. GitHub does serve
		// fetch-by-SHA for commits reachable from any advertised ref, pull refs
		// included, so ask for the object itself. Best-effort: if this fails too,
		// the worktree add below reports the unresolvable ref as before.
		fetchRefDirect(repoDir, ref)
	}
	// Reuse an existing worktree at this path instead of failing. On a FAILED run the
	// worktree is deliberately kept (see removeUnlessFailed) so the teardown can destroy
	// against the deployed code — but Teardown then calls prepareWorktree again, and
	// `git worktree add` on an existing path exits 128 ("already exists"). That aborted
	// teardown before it ran, so a failed run never destroyed itself and never captured
	// diagnostics; only the external cleanup backstop saved it. Verify the path is a
	// real checkout of the same ref before trusting it.
	if fi, serr := os.Stat(filepath.Join(dir, ".git")); serr == nil && fi != nil {
		if err := run(dir, "git", "checkout", "--detach", checkout); err != nil {
			return nil, fmt.Errorf("reuse worktree %s at %s: %w", ref, dir, err)
		}
		return &Worktree{Dir: dir, Ref: ref}, nil
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

// removeUnlessFailed removes the worktree only when the run succeeded
// (*runErr == nil). On failure we KEEP it: the applied-worktree marker points at
// this worktree, and the out-of-process cleanup-orphans backstop destroys against
// the marked worktree. If we removed it here, the backstop would find a stale
// marker and fall back to the LIVE tree (current branch's code/refs), which won't
// match the deployed stack and strands the infra. Mirrors the marker's own
// keep-on-failure rule. Stale kept worktrees are 'git worktree remove'-able after cleanup.
func (w *Worktree) removeUnlessFailed(repoDir string, runErr *error) {
	if runErr != nil && *runErr != nil {
		fmt.Fprintf(os.Stderr, "\n>> keeping worktree %s (run failed) so the cleanup backstop can destroy against the deployed code; 'git worktree remove' it after cleanup\n", w.Dir)
		return
	}
	_ = w.Remove(repoDir)
}

func (w *Worktree) HasSubmodule() bool {
	_, err := os.Stat(filepath.Join(w.Dir, ".gitmodules"))
	return err == nil
}

// resolvesToCommit reports whether ref already names a commit in the local object
// store (tag, SHA, or any other committish).
func resolvesToCommit(repoDir, ref string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

// fetchRefDirect fetches a single ref (typically a raw SHA) from origin. Same HTTPS
// rewrite and hard timeout as FetchOrigin, and non-fatal for the same reason: the
// caller's checkout produces the authoritative error if the ref stays unresolvable.
func fetchRefDirect(repoDir, ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git",
		"-c", "url.https://github.com/.insteadOf=git@github.com:",
		"fetch", "origin", ref)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, ">> warning: direct fetch of %s from origin failed: %v\n", ref, err)
	}
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

// TeardownRepoRef returns the ref a teardown should resolve its configuration against:
// the manifest's AppliedRef, the ref whose code matches the deployed state.
//
// Why this exists as a default rather than a runbook line (Lodestar-1xm.36.5): a
// teardown re-resolves the matrix config, the env path and the identifier against
// whatever ref the pod checked out, and the caller had to remember to pass the run's
// frozen ref. Forgetting it is silent - a same-named config at a different ref resolves
// to plausible-looking, wrong targets, and the destroy then reports success against a
// stack it never touched. The manifest already records the right answer.
//
// Falls back to ToRef exactly as teardownRefAndSide does, for the case this run hit:
// a provision killed mid-apply never reaches its `rm.AppliedRef = applyRef` write, so
// AppliedRef is empty on precisely the runs whose teardown matters most. Returns ""
// when the manifest records neither, which the caller must read as "no opinion, keep
// whatever ref you already have" rather than as a ref.
func TeardownRepoRef(rm *RunManifest) string {
	if rm == nil {
		return ""
	}
	if rm.AppliedRef != "" {
		return rm.AppliedRef
	}
	return rm.ToRef
}
