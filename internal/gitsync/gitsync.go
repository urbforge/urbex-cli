// Package gitsync runs the git operations urbex needs to keep Gitea the
// source of truth: committing the GitOps repo working directory after
// every change and pushing it, and creating an app repo's environment
// branch on Gitea so Komodo has something to build. See docs/decisions/0004-gitops-gitea-komodo.md and
// docs/decisions/0018-image-build-komodo-gitea-registry.md in
// urbforge/urbex.
package gitsync

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitignore lists paths in the GitOps repo that must never be committed:
// provider plugins downloaded by terraform init.
const gitignore = ".terraform/\n"

// CommitAll stages everything in dir and commits it with message, first
// running `git init` (branch main) and adding a .gitignore if dir isn't a
// repo yet. It returns false if there was nothing to commit.
func CommitAll(ctx context.Context, dir, message string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if _, err := git(ctx, dir, nil, "init", "-q", "-b", "main"); err != nil {
			return false, err
		}
	}
	ignorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		if err := os.WriteFile(ignorePath, []byte(gitignore), 0o644); err != nil {
			return false, err
		}
	}
	if _, err := git(ctx, dir, nil, "add", "-A"); err != nil {
		return false, err
	}
	if _, err := git(ctx, dir, nil, "diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	args := []string{"commit", "-q", "-m", message}
	if email, _ := git(ctx, dir, nil, "config", "user.email"); email == "" {
		args = append([]string{"-c", "user.name=urbex", "-c", "user.email=urbex@localhost"}, args...)
	}
	if _, err := git(ctx, dir, nil, args...); err != nil {
		return false, err
	}
	return true, nil
}

// Push pushes refspecs from the repo in dir to remoteURL (an http(s)
// Gitea clone URL), authenticating with token. The token travels in an
// environment-provided http.extraHeader, so it never appears in the
// process list or in the repo's config.
func Push(ctx context.Context, dir, remoteURL, token string, refspecs ...string) error {
	auth := base64.StdEncoding.EncodeToString([]byte("urbex:" + token))
	env := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + auth,
		"GIT_TERMINAL_PROMPT=0",
	}
	args := append([]string{"push", "-q", remoteURL}, refspecs...)
	_, err := git(ctx, dir, env, args...)
	return err
}

// HeadCommit returns the full SHA of HEAD in the repo at dir.
func HeadCommit(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, nil, "rev-parse", "HEAD")
}

func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
