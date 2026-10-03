package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCommitAllThenPush(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".terraform", "plugin"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "urbex.platform.yaml"), []byte("domain: example.com\n"), 0o644)

	committed, err := CommitAll(ctx, dir, "first")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !committed {
		t.Fatal("expected a first commit")
	}
	tracked, err := git(ctx, dir, nil, "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	if tracked != ".gitignore\nurbex.platform.yaml" {
		t.Errorf("unexpected tracked files (.terraform/ must be ignored):\n%s", tracked)
	}

	committed, err = CommitAll(ctx, dir, "nothing changed")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if committed {
		t.Error("expected no commit when nothing changed")
	}

	remote := t.TempDir()
	if _, err := git(ctx, remote, nil, "init", "-q", "--bare"); err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, dir, remote, "token", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	head, err := HeadCommit(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	pushed, err := git(ctx, remote, nil, "rev-parse", "main")
	if err != nil {
		t.Fatal(err)
	}
	if pushed != head {
		t.Errorf("remote main = %s, want %s", pushed, head)
	}
}

func TestPull_RebasesLocalCommitsOntoOthersPushes(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if _, err := git(ctx, remote, nil, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatal(err)
	}

	// A remote that doesn't have the branch yet is fine.
	mine := t.TempDir()
	os.WriteFile(filepath.Join(mine, "a"), []byte("1"), 0o644)
	if _, err := CommitAll(ctx, mine, "first"); err != nil {
		t.Fatal(err)
	}
	if err := Pull(ctx, mine, remote, "token", "main"); err != nil {
		t.Fatalf("Pull from an empty remote: %v", err)
	}
	if err := Push(ctx, mine, remote, "token", "HEAD:refs/heads/main"); err != nil {
		t.Fatal(err)
	}

	// Someone else - Komodo, say - pushes...
	theirs := t.TempDir()
	if _, err := git(ctx, theirs, nil, "clone", "-q", remote, "."); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(theirs, "b"), []byte("2"), 0o644)
	if _, err := CommitAll(ctx, theirs, "theirs"); err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, theirs, remote, "token", "HEAD:refs/heads/main"); err != nil {
		t.Fatal(err)
	}

	// ...while we commit locally, with an uncommitted change on top.
	os.WriteFile(filepath.Join(mine, "c"), []byte("3"), 0o644)
	if _, err := CommitAll(ctx, mine, "mine"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mine, "a"), []byte("uncommitted"), 0o644)

	if err := Pull(ctx, mine, remote, "token", "main"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if err := Push(ctx, mine, remote, "token", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("Push after Pull must fast-forward: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(mine, "b")); string(data) != "2" {
		t.Error("expected their commit to be pulled")
	}
	if data, _ := os.ReadFile(filepath.Join(mine, "a")); string(data) != "uncommitted" {
		t.Error("uncommitted changes must survive a pull")
	}
}
