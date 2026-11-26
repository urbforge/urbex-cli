package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/doctor"
	"github.com/urbforge/urbex-cli/internal/grafana"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/projectrepo"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
	"github.com/urbforge/urbex-cli/internal/version"
)

// errDoctorFailed makes urbex doctor exit non-zero when a check failed;
// the report says which.
var errDoctorFailed = fmt.Errorf("some checks failed")

func newDoctorCmd() *cobra.Command {
	var gitopsRepoPath, manifestPath, repoDir string
	var testAlerts bool
	var output outputFlag

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine, the workspace, Proxmox and Cloudflare have what urbex needs",
		Long: `Checks, in order: the tools urbex runs (git, ssh, terraform,
ansible-playbook, sops, age); the workspace (the credentials, the SSH key,
the GitOps repo and its urbex.platform.yaml); with a platform config and a
Proxmox token, the node (the API answers, the node, pool, storage, LXC
template and bridge exist, the token has the privileges urbex needs, and the optional
read-only token for the node's metrics); and,
with Cloudflare in the config, the token and the zone; with
observability.alerts, the channels' credentials; and, run in a project's
folder, that its urbex.yaml is committed in a git repository and, when
it declares devices, that each application's LXC has them.

It starts with urbex's own version. Each check is ok, warn, fail or skip, with what to do when it isn't ok. It
exits non-zero when a check failed. It changes nothing, and prints no
secret. --test-alerts also sends a test message through each alert
channel, from Grafana. --output json prints the report as one JSON document. See
docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sys := doctor.LocalSystem()
			r := &doctor.Report{Version: version.Get().String()}
			doctor.Tools(ctx, sys, r)

			ws := doctor.Workspace{}
			ws.CredentialsFile, _ = credentials.DefaultPath()
			creds, err := credentials.Load()
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
			ws.Missing = creds.MissingForProvisioning()
			ws.GitOpsRepo, ws.GitOpsRepoErr = resolveGitOpsRepoPath(gitopsRepoPath)
			var cfg *platform.Config
			if ws.GitOpsRepoErr == nil {
				path := filepath.Join(ws.GitOpsRepo, "urbex.platform.yaml")
				if _, err := os.Stat(path); err == nil {
					if cfg, ws.ConfigLoad = platform.LoadFile(path); ws.ConfigLoad == nil {
						ws.ConfigErrs = cfg.Validate()
					}
				}
			}
			ws.CredentialsShown = credentials.HostPath(ws.CredentialsFile)
			doctor.WorkspaceChecks(sys, ws, r)
			if cfg != nil && len(ws.ConfigErrs) > 0 {
				cfg = nil // the checks below would read values known to be wrong
			}

			var p doctor.ProxmoxReader
			if cfg != nil && creds.ProxmoxToken != "" {
				p = proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxToken, cfg.Proxmox.Insecure)
			}
			doctor.ProxmoxChecks(ctx, p, cfg, r)
			var mon doctor.PermissionsReader
			if cfg != nil && creds.ProxmoxMonitorToken != "" {
				mon = proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxMonitorToken, cfg.Proxmox.Insecure)
			}
			doctor.NodeMetricsChecks(ctx, mon, creds.ProxmoxMonitorToken, cfg, r)
			var bk doctor.BackupReader
			if cfg != nil && creds.ProxmoxBackupToken != "" {
				bk = proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxBackupToken, cfg.Proxmox.Insecure)
			}
			doctor.BackupChecks(ctx, bk, creds.ProxmoxBackupToken, cfg, r)
			var cf doctor.CloudflareReader
			if cfg != nil && creds.CloudflareToken != "" {
				cf = cloudflare.New(cloudflare.DefaultBaseURL, creds.CloudflareToken, cfg.Cloudflare.AccountID, cfg.Cloudflare.ZoneID)
			}
			doctor.CloudflareChecks(ctx, cf, cfg, r)
			var at doctor.AlertsTester
			if cfg != nil && testAlerts {
				ep, err := bootstrap.EndpointsFor(cfg)
				if err != nil {
					return err
				}
				at = grafana.New(ep.Grafana, "admin", creds.GrafanaAdminPassword)
			}
			doctor.AlertsChecks(ctx, at, cfg, creds, r)
			if _, err := os.Stat(manifestPath); err == nil {
				doctor.ProjectChecks(manifestPath, true, projectrepo.Check(ctx, repoDir, manifestPath), r)
			}
			if cfg != nil {
				machines, err := deviceMachines(manifestPath, ws.GitOpsRepo, cfg)
				if err != nil {
					r.Checks = append(r.Checks, doctor.Check{Group: "devices", Name: manifestPath, Status: doctor.Warn, Detail: err.Error()})
				}
				dr, _ := p.(doctor.DevicesReader)
				doctor.DevicesChecks(ctx, dr, cfg.Proxmox.Node, machines, r)
			}

			r.OK = !r.Failed()
			out := cmd.OutOrStdout()
			if output.JSON() {
				if err := writeJSON(out, r); err != nil {
					return err
				}
			} else {
				printDoctor(out, r)
			}
			if !r.OK {
				return errDoctorFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&testAlerts, "test-alerts", false, "send a test message through each channel of observability.alerts")
	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest, when run in a project: that it is committed, and its devices (skipped when there is none)")
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "path to the app's git repo, which urbex.yaml must be committed in")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	output.Register(cmd)
	return cmd
}

// printDoctor prints the report, a group at a time, with the fix under
// each check that isn't ok.
func printDoctor(out io.Writer, r *doctor.Report) {
	if r.Version != "" {
		fmt.Fprintf(out, "%s\n\n", r.Version)
	}
	group := ""
	for _, c := range r.Checks {
		if c.Group != group {
			if group != "" {
				fmt.Fprintln(out)
			}
			group = c.Group
			fmt.Fprintf(out, "%s:\n", c.Group)
		}
		fmt.Fprintf(out, "  %-5s %-18s %s\n", c.Status, c.Name, c.Detail)
		if c.Fix != "" && c.Status != doctor.OK {
			fmt.Fprintf(out, "        %-18s -> %s\n", "", c.Fix)
		}
	}
	fmt.Fprintf(out, "\n%s\n", r.Summary())
}

// deviceMachines lists the machines of the project in manifestPath whose
// applications declare devices, in each of its environments - an
// application's own, or its group's - with their VMIDs from the
// allocation ledger: nothing without a manifest.
func deviceMachines(manifestPath, repoPath string, cfg *platform.Config) ([]doctor.DeviceMachine, error) {
	if _, err := os.Stat(manifestPath); err != nil {
		return nil, nil
	}
	m, _, err := manifest.LoadTyped(manifestPath)
	if err != nil || m == nil {
		return nil, fmt.Errorf("%s isn't valid: 'urbex plan <env>' says why", manifestPath)
	}
	ledger, err := allocation.Load(filepath.Join(repoPath, "state", "allocations.json"), cfg.Proxmox.VMIDBase+100, cfg.Proxmox.Network.BaseHostOffset+10)
	if err != nil {
		return nil, fmt.Errorf("loading the allocation ledger: %w", err)
	}
	// One check per machine: the applications of a group (lxc:) share
	// their LXC and its devices.
	var out []doctor.DeviceMachine
	for _, env := range m.EnvironmentList() {
		at := map[string]int{}
		for _, svc := range m.Services {
			if len(svc.Devices) == 0 {
				continue
			}
			host := provision.MachineHostname(m.Project, svc, env)
			i, ok := at[host]
			if !ok {
				entry, _ := ledger.Lookup(host)
				out = append(out, doctor.DeviceMachine{Service: svc.Group(), Env: env, VMID: entry.VMID, VM: svc.IsVM()})
				i = len(out) - 1
				at[host] = i
			}
			for _, d := range svc.Devices {
				if !slices.Contains(out[i].Devices, d) {
					out[i].Devices = append(out[i].Devices, d)
				}
			}
		}
	}
	return out, nil
}
