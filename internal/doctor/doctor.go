// Package doctor answers `urbex doctor`: whether this machine and its
// workspace have what urbex needs - the tools, the credentials, the
// GitOps repo and its platform config - and whether the Proxmox node and
// Cloudflare answer with the access urbex needs. Each check has a
// status and, when it isn't ok, what to do. It reads and changes
// nothing else (ADR-0032).
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The statuses of a check.
const (
	OK   = "ok"
	Warn = "warn" // works, but something is worth knowing or doing
	Fail = "fail" // urbex can't do its work until it is fixed
	Skip = "skip" // not applicable, or not checkable yet
)

// Check is one line of the report.
type Check struct {
	Group  string `json:"group"` // tools, workspace, proxmox, cloudflare
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Report is every check, in order.
type Report struct {
	Checks []Check `json:"checks"`
	OK     bool    `json:"ok"` // no check failed
}

func (r *Report) add(c Check) {
	r.Checks = append(r.Checks, c)
}

// Failed reports whether a check failed.
func (r *Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// System is what the checks read of this machine; tests replace it.
type System struct {
	LookPath func(string) (string, error)
	// Output runs a command and returns its output.
	Output func(ctx context.Context, name string, args ...string) (string, error)
	Exists func(path string) bool
	Getenv func(string) string
	Home   string
}

// LocalSystem is this machine.
func LocalSystem() System {
	home, _ := os.UserHomeDir()
	return System{
		LookPath: exec.LookPath,
		Output: func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return string(out), err
		},
		Exists: func(path string) bool { _, err := os.Stat(path); return err == nil },
		Getenv: os.Getenv,
		Home:   home,
	}
}

// tool is a program urbex runs, and how to ask its version.
type tool struct {
	name, versionArg, why string
}

var tools = []tool{
	{"git", "--version", "pushes the GitOps repo and the projects"},
	{"ssh", "-V", "Ansible reaches the LXCs with it"},
	{"terraform", "version", "creates the LXCs"},
	{"ansible-playbook", "--version", "configures the LXCs"},
	{"sops", "--version", "encrypts the secrets"},
	{"age", "--version", "the secrets' key"},
}

// Tools checks the programs urbex runs. In urbex-op's image they are all
// there; natively they come from mise (mise.toml of urbex-cli).
func Tools(ctx context.Context, sys System, r *Report) {
	for _, t := range tools {
		if _, err := sys.LookPath(t.name); err != nil {
			r.add(Check{Group: "tools", Name: t.name, Status: Fail, Detail: "not found: urbex needs it (" + t.why + ")",
				Fix: "run urbex through urbex-op, whose image has every tool; natively, 'mise install' in urbex-cli"})
			continue
		}
		out, _ := sys.Output(ctx, t.name, t.versionArg)
		r.add(Check{Group: "tools", Name: t.name, Status: OK, Detail: firstLine(out)})
	}
}

// Workspace is what the workspace checks look at.
type Workspace struct {
	CredentialsFile string // ~/.urbex/credentials.yaml
	// CredentialsShown is where the report says it is: through urbex-op,
	// its path on the operator's machine; "" for CredentialsFile.
	CredentialsShown string
	// Missing are the credentials provisioning needs that are absent
	// (credentials.MissingForProvisioning).
	Missing []string
	// GitOpsRepo is the GitOps repo's working copy, or "" with
	// GitOpsRepoErr when none is given.
	GitOpsRepo    string
	GitOpsRepoErr error
	// ConfigErrs are the platform config's validation errors; nil when it
	// is valid, or absent.
	ConfigErrs []string
	ConfigLoad error
}

// WorkspaceChecks checks the credentials, the SSH key, the GitOps repo
// and its platform config.
func WorkspaceChecks(sys System, ws Workspace, r *Report) {
	shown := ws.CredentialsShown
	if shown == "" {
		shown = ws.CredentialsFile
	}
	if sys.Exists(ws.CredentialsFile) {
		r.add(Check{Group: "workspace", Name: "credentials file", Status: OK, Detail: shown})
	} else {
		r.add(Check{Group: "workspace", Name: "credentials file", Status: Warn, Detail: shown + " doesn't exist yet",
			Fix: "store the Proxmox token: 'urbex-op set proxmoxToken' (or URBEX_PROXMOX_TOKEN)"})
	}
	if len(ws.Missing) == 0 {
		r.add(Check{Group: "workspace", Name: "credentials", Status: OK, Detail: "the Proxmox token and the age key are set"})
	} else {
		r.add(Check{Group: "workspace", Name: "credentials", Status: Fail, Detail: "missing: " + strings.Join(ws.Missing, ", "),
			Fix: "'urbex-op set proxmoxToken' for the token; the age key is the workspace's home/age.key ('urbex-op init' makes it) - see docs/reference/credentials.md"})
	}

	switch {
	case sys.Getenv("SSH_AUTH_SOCK") != "":
		r.add(Check{Group: "workspace", Name: "SSH key", Status: OK, Detail: "an SSH agent"})
	case sys.Exists(filepath.Join(sys.Home, ".ssh", "urbex")):
		r.add(Check{Group: "workspace", Name: "SSH key", Status: OK, Detail: filepath.Join(sys.Home, ".ssh", "urbex")})
	case sys.Exists(filepath.Join(sys.Home, ".ssh", "id_ed25519")):
		r.add(Check{Group: "workspace", Name: "SSH key", Status: OK, Detail: filepath.Join(sys.Home, ".ssh", "id_ed25519")})
	default:
		r.add(Check{Group: "workspace", Name: "SSH key", Status: Warn, Detail: "no SSH agent and no key in ~/.ssh: Ansible can't reach the LXCs",
			Fix: "'urbex-op init' creates home/.ssh/urbex; natively, load the key matching proxmox.sshPublicKey in an agent"})
	}

	if ws.GitOpsRepoErr != nil {
		r.add(Check{Group: "workspace", Name: "GitOps repo", Status: Fail, Detail: ws.GitOpsRepoErr.Error(),
			Fix: "set URBEX_GITOPS_REPO, or pass --gitops-repo (urbex-op uses the workspace's gitops/)"})
		return
	}
	r.add(Check{Group: "workspace", Name: "GitOps repo", Status: OK, Detail: ws.GitOpsRepo})
	config := filepath.Join(ws.GitOpsRepo, "urbex.platform.yaml")
	switch {
	case !sys.Exists(config):
		r.add(Check{Group: "workspace", Name: "platform config", Status: Warn, Detail: config + " doesn't exist yet",
			Fix: "'urbex discover' proposes one; 'urbex bootstrap' writes a starter one"})
	case ws.ConfigLoad != nil:
		r.add(Check{Group: "workspace", Name: "platform config", Status: Fail, Detail: ws.ConfigLoad.Error(), Fix: "fix the YAML in " + config})
	case len(ws.ConfigErrs) > 0:
		r.add(Check{Group: "workspace", Name: "platform config", Status: Fail, Detail: strings.Join(ws.ConfigErrs, "; "),
			Fix: "edit " + config + " - see docs/reference/platform-config.md"})
	default:
		r.add(Check{Group: "workspace", Name: "platform config", Status: OK, Detail: config})
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// Summary is the report's last line.
func (r *Report) Summary() string {
	counts := map[string]int{}
	for _, c := range r.Checks {
		counts[c.Status]++
	}
	if counts[Fail] > 0 {
		return fmt.Sprintf("%d failed, %d warnings: urbex can't work until the failures are fixed.", counts[Fail], counts[Warn])
	}
	if counts[Warn] > 0 {
		return fmt.Sprintf("No failures, %d warnings.", counts[Warn])
	}
	return "Everything urbex needs is there."
}
