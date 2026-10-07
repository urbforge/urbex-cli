package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestMain runs the tests with no git identity, whatever the machine's
// git configuration: user.useConfigOnly makes git refuse to guess one
// from the user and host names, as it can't on many servers and CI.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gitsync-home")
	if err != nil {
		panic(err)
	}
	config := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(config, []byte("[user]\n\tuseConfigOnly = true\n"), 0o644); err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("GIT_CONFIG_GLOBAL", config)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

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
	mine, remote := divergedRepos(t, nil)
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

// A configured identity is the one the rebased commits get.
func TestPull_KeepsAConfiguredIdentity(t *testing.T) {
	ctx := context.Background()
	mine, remote := divergedRepos(t, func(dir string) {
		git(ctx, dir, nil, "config", "user.name", "Ada")
		git(ctx, dir, nil, "config", "user.email", "ada@example.com")
	})
	if err := Pull(ctx, mine, remote, "token", "main"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	committer, err := git(ctx, mine, nil, "log", "-1", "--format=%cn <%ce>")
	if err != nil {
		t.Fatal(err)
	}
	if committer != "Ada <ada@example.com>" {
		t.Errorf("rebased commit's committer = %q, want the configured identity", committer)
	}
}

// divergedRepos makes a bare remote and a local clone, mine, where both
// moved since mine last pulled: someone else (Komodo, say) pushed, while
// mine has a local commit and an uncommitted change. configure, if set,
// runs on mine before its first commit.
func divergedRepos(t *testing.T, configure func(dir string)) (mine, remote string) {
	t.Helper()
	ctx := context.Background()
	remote = t.TempDir()
	if _, err := git(ctx, remote, nil, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatal(err)
	}

	// A remote that doesn't have the branch yet is fine.
	mine = t.TempDir()
	if configure != nil {
		git(ctx, mine, nil, "init", "-q", "-b", "main")
		configure(mine)
	}
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

	return mine, remote
}
