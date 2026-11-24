package projectrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ctx := context.Background()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "urbex.yaml")
	if err := os.WriteFile(manifest, []byte("project: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	expect := func(want string) {
		t.Helper()
		err := Check(ctx, dir, manifest)
		switch {
		case want == "" && err != nil:
			t.Errorf("expected ok, got %v", err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("expected %q, got %v", want, err)
		}
	}

	expect("isn't in a git repository")
	if err := Check(ctx, dir, manifest); err == nil || !strings.Contains(err.Error(), Fix) {
		t.Errorf("the error should say what to run: %v", err)
	}
	git("init", "-q", "-b", "main")
	expect("has no commit yet")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README")
	git("commit", "-q", "-m", "readme")
	expect("urbex.yaml isn't committed")
	git("add", "urbex.yaml")
	git("commit", "-q", "-m", "urbex.yaml")
	expect("")
	// A change not committed yet is fine: apply reads the file, and the
	// branch on Forgejo comes from HEAD only the first time.
	if err := os.WriteFile(manifest, []byte("project: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect("")
}
