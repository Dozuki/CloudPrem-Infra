package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func AddWorktree(repoDir, baseDir, ref string, initSubmodules bool) (*Worktree, error) {
	dir := filepath.Join(baseDir, sanitize(ref))
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	if err := run(repoDir, "git", "worktree", "add", "--detach", dir, ref); err != nil {
		return nil, fmt.Errorf("worktree add %s: %w", ref, err)
	}
	if initSubmodules {
		if err := run(dir, "git", "submodule", "update", "--init", "--recursive"); err != nil {
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

func sanitize(ref string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(ref)
}

func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
