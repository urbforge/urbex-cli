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

func TestIsDirty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), []byte("1"), 0o644)
	if _, err := CommitAll(ctx, dir, "init"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "untracked"), []byte("x"), 0o644)
	if dirty, err := IsDirty(ctx, dir); err != nil || dirty {
		t.Errorf("untracked files alone must not count as dirty: dirty=%v err=%v", dirty, err)
	}
	os.WriteFile(filepath.Join(dir, "a"), []byte("2"), 0o644)
	if dirty, err := IsDirty(ctx, dir); err != nil || !dirty {
		t.Errorf("expected dirty after modifying a tracked file: dirty=%v err=%v", dirty, err)
	}
}
