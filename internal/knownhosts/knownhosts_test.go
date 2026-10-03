package knownhosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForget_RemovesOnlyTheGivenHosts(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "state"), 0o755)
	content := strings.Join([]string{
		"192.168.1.200 ssh-ed25519 AAAAold-gitea",
		"192.168.1.200 ecdsa-sha2-nistp256 AAAAold-gitea-ecdsa",
		"[192.168.1.201]:22 ssh-ed25519 AAAAold-komodo",
		"192.168.1.2000 ssh-ed25519 AAAAnot-the-same-host",
		"other.lan,192.168.1.210 ssh-ed25519 AAAAproject",
		"# a comment",
	}, "\n") + "\n"
	os.WriteFile(Path(repo), []byte(content), 0o644)

	if err := Forget(repo, "192.168.1.200", "192.168.1.201", "192.168.1.210"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(Path(repo))
	want := "192.168.1.2000 ssh-ed25519 AAAAnot-the-same-host\n# a comment\n"
	if string(got) != want {
		t.Errorf("known_hosts =\n%s\nwant\n%s", got, want)
	}
}

func TestForget_NoFileIsANoOp(t *testing.T) {
	if err := Forget(t.TempDir(), "192.168.1.200"); err != nil {
		t.Fatal(err)
	}
}

func TestSSHArgs_UseTheRepoFileInPlainText(t *testing.T) {
	args, err := SSHArgs("/srv/gitops")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"StrictHostKeyChecking=accept-new", "HashKnownHosts=no", `UserKnownHostsFile="/srv/gitops/state/known_hosts"`} {
		if !strings.Contains(args, want) {
			t.Errorf("%q missing %q", args, want)
		}
	}
}
