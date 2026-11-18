package bootstrap

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/credentials"
)

// gitopsRepo returns a git working copy with one commit, as bootstrap
// leaves it.
func gitopsRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func TestTeardown_RefusesWhileProjectEnvironmentsExist(t *testing.T) {
	o, runner, _ := newRunOrchestrator(t, &fakeProxmox{})
	dir := gitopsRepo(t)
	os.MkdirAll(filepath.Join(dir, "environments", "staging", "hello", "api"), 0o755)

	err := o.Teardown(context.Background(), TeardownOptions{GitOpsRepoPath: dir, Yes: true})
	if err == nil || !strings.Contains(err.Error(), "hello (staging)") || !strings.Contains(err.Error(), "urbex destroy") {
		t.Fatalf("expected a refusal naming the environment, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("nothing should run, got %+v", runner.calls)
	}
}

func TestTeardown_WithoutYesOnlyReports(t *testing.T) {
	o, runner, fp := newRunOrchestrator(t, &fakeProxmox{})
	o.Credentials.ForgeToken = "tok"
	dir := gitopsRepo(t)

	err := o.Teardown(context.Background(), TeardownOptions{GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected to be asked for --yes, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("nothing should run without --yes, got %+v", runner.calls)
	}
	if len(fp.pulled) != 1 {
		t.Errorf("expected the gitops repo to be pulled from Gitea first, got %v", fp.pulled)
	}
	out := o.Out.(*bytes.Buffer).String()
	for _, want := range []string{"urbex-gitea", "urbex-komodo", "every repository and image", "only copy"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the report:\n%s", want, out)
		}
	}
}

func TestTeardown_DestroysBaseServicesAndForgetsGeneratedCredentials(t *testing.T) {
	o, runner, _ := newRunOrchestrator(t, &fakeProxmox{})
	dir := gitopsRepo(t)
	if err := o.Credentials.Update(func(c *credentials.Credentials) { c.KomodoAPIKey = "old-key" }); err != nil {
		t.Fatal(err)
	}

	if err := o.Teardown(context.Background(), TeardownOptions{GitOpsRepoPath: dir, Yes: true}); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	var args []string
	for _, c := range runner.calls {
		if !strings.HasSuffix(c.Dir, filepath.Join("terraform", "base")) {
			t.Errorf("terraform must run in terraform/base, ran in %s", c.Dir)
		}
		args = append(args, strings.Join(c.Args, " "))
	}
	if strings.Join(args, ",") != "init,destroy -auto-approve -parallelism=2" {
		t.Errorf("unexpected terraform calls: %v", args)
	}
	if o.Credentials.KomodoAPIKey != "" || o.Credentials.ForgeToken != "" {
		t.Error("the generated credentials of the destroyed platform should be forgotten")
	}
	if o.Credentials.ProxmoxToken == "" || o.Credentials.AgeKey == "" {
		t.Error("the credentials the user provided must be kept")
	}
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%s").Output()
	if err != nil || strings.TrimSpace(string(out)) != "urbex teardown" {
		t.Errorf("expected the gitops repo committed locally, last commit %q (%v)", out, err)
	}
}

// idProxmox also lists its machines' VMIDs, as proxmoxapi.Client does.
type idProxmox struct {
	fakeProxmox
	ids []int
}

func (p *idProxmox) ListContainerIDs(context.Context, string) ([]int, error) { return p.ids, nil }

// With no environment left, teardown forgets the ledger entries whose
// machines are gone from Proxmox, and stops on one whose machine still
// exists, naming it (urbforge/urbex-cli#115).
func TestForgetStaleLedger(t *testing.T) {
	write := func(dir string) {
		os.MkdirAll(filepath.Join(dir, "state"), 0o755)
		os.WriteFile(filepath.Join(dir, "state", "allocations.json"), []byte(`{"entries":{"pgtest-board-staging":{"vmid":9129,"ip":"192.168.1.239"},"shop-api-prod":{"vmid":9130,"ip":"192.168.1.240"}},"nextVmid":9131,"nextIpOffset":241}`), 0o644)
	}
	for _, tc := range []struct {
		name    string
		ids     []int
		wantErr string
		left    string
	}{
		{"all gone", []int{9000, 9001}, "", ""},
		{"one still exists", []int{9130}, "shop-api-prod (VMID 9130)", "shop-api-prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
			o.Proxmox = &idProxmox{ids: tc.ids}
			var out bytes.Buffer
			o.Out = &out
			dir := t.TempDir()
			write(dir)
			err := o.forgetStaleLedger(context.Background(), dir)
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if !strings.Contains(out.String(), "pgtest-board-staging (VMID 9129)") {
				t.Errorf("output should name the forgotten entry: %q", out.String())
			}
			data, _ := os.ReadFile(filepath.Join(dir, "state", "allocations.json"))
			if strings.Contains(string(data), "pgtest-board-staging") || (tc.left != "" && !strings.Contains(string(data), tc.left)) {
				t.Errorf("ledger after: %s", data)
			}
		})
	}
}
