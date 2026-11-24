// Package projectrepo checks that a project's urbex.yaml is committed in
// a git repository: urbex creates the project's repo on Forgejo from
// that repository's HEAD the first time it applies an environment, and
// the project's changes - urbex.yaml's among them - go through it.
package projectrepo

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Fix is what to run in the project's folder when the check fails.
const Fix = `git init -b main && git add urbex.yaml && git commit -m "urbex.yaml"`

// Check returns nil when manifestPath is tracked by git in the
// repository at repoDir, which has a commit; otherwise an error that
// says what is missing and how to fix it.
func Check(ctx context.Context, repoDir, manifestPath string) error {
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repoDir}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	name := filepath.Base(manifestPath)
	if _, err := git("rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("%s isn't in a git repository (--repo-dir %s): urbex creates the project's repo on Forgejo from it. In the project's folder, run: %s", name, repoDir, Fix)
	}
	if _, err := git("rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return fmt.Errorf("the git repository at %s has no commit yet: commit %s first (git add %s && git commit -m %q)", repoDir, name, name, name)
	}
	abs, err := filepath.Abs(manifestPath)
	if err != nil {
		return err
	}
	if _, err := git("ls-files", "--error-unmatch", "--", abs); err != nil {
		return fmt.Errorf("%s isn't committed in the git repository at %s: run git add %s && git commit -m %q", manifestPath, repoDir, name, name)
	}
	return nil
}
