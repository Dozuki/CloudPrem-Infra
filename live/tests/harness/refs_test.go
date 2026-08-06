package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRef(t *testing.T) {
	tags := []string{"v6.0", "v5.3", "v5.2"}
	cases := map[string]string{
		"auto:latest":   "v6.0",
		"auto:latest-1": "v5.3",
		"v6.1-release":  "v6.1-release",
	}
	for in, want := range cases {
		got, err := ResolveRef(in, tags)
		if err != nil {
			t.Fatalf("ResolveRef(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ResolveRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveRefNotEnoughTags(t *testing.T) {
	if _, err := ResolveRef("auto:latest-1", []string{"v6.0"}); err == nil {
		t.Errorf("expected error when fewer than 2 tags for auto:latest-1")
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{"-c", "user.email=harness@test", "-c", "user.name=harness",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=master"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A matrix run carries the PR's HEAD SHA, and GitHub deletes the branch when the PR
// merges, so by the time an upgrade or teardown phase asks for that SHA a normal
// `fetch origin --tags --prune` no longer retrieves it (it lives only under
// refs/pull/*/head, which is never fetched). AddWorktree must fall back to fetching
// the object directly; before it did, every post-merge teardown died with
// "fatal: invalid reference" and leaked its stack.
func TestAddWorktreeFetchesPostMergePRHeadSHA(t *testing.T) {
	upstream := t.TempDir()
	gitT(t, upstream, "init", "-q")
	writeTestFile(t, upstream, "a.txt", "one")
	gitT(t, upstream, "add", ".")
	gitT(t, upstream, "commit", "-qm", "one")

	local := filepath.Join(t.TempDir(), "clone")
	gitT(t, "", "clone", "-q", upstream, local)

	// The PR branch: a commit the clone never saw, whose branch is deleted after
	// "merge". allowAnySHA1InWant stands in for GitHub's serve-by-SHA behavior.
	gitT(t, upstream, "checkout", "-qb", "pr-head")
	writeTestFile(t, upstream, "b.txt", "two")
	gitT(t, upstream, "add", ".")
	gitT(t, upstream, "commit", "-qm", "two")
	sha := gitT(t, upstream, "rev-parse", "HEAD")
	gitT(t, upstream, "checkout", "-q", "master")
	gitT(t, upstream, "branch", "-qD", "pr-head")
	gitT(t, upstream, "config", "uploadpack.allowAnySHA1InWant", "true")

	FetchOrigin(local)
	if exec.Command("git", "-C", local, "rev-parse", "--verify", "--quiet", sha+"^{commit}").Run() == nil {
		t.Fatalf("fixture broken: %s resolves in the clone before AddWorktree", sha)
	}

	wt, err := AddWorktree(local, filepath.Join(local, "live", "tests", "__worktrees__", "t"), sha, false)
	if err != nil {
		t.Fatalf("AddWorktree(%s): %v", sha, err)
	}
	if got := gitT(t, wt.Dir, "rev-parse", "HEAD"); got != sha {
		t.Errorf("worktree HEAD = %s, want %s", got, sha)
	}

	// Teardown re-enters AddWorktree for the same ref (the worktree is kept on
	// failure), taking the reuse branch. The fallback must sit ahead of BOTH
	// branches, so re-entry after the object landed has to keep working.
	wt2, err := AddWorktree(local, filepath.Join(local, "live", "tests", "__worktrees__", "t"), sha, false)
	if err != nil {
		t.Fatalf("AddWorktree reuse (%s): %v", sha, err)
	}
	if wt2.Dir != wt.Dir {
		t.Errorf("reuse created a second worktree: %s vs %s", wt2.Dir, wt.Dir)
	}
}
