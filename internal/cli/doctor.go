package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/doctor"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// errDoctorFailed makes urbex doctor exit non-zero when a check failed;
// the report says which.
var errDoctorFailed = fmt.Errorf("some checks failed")

func newDoctorCmd() *cobra.Command {
	var gitopsRepoPath string
	var output outputFlag

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine, the workspace, Proxmox and Cloudflare have what urbex needs",
		Long: `Checks, in order: the tools urbex runs (git, ssh, terraform,
ansible-playbook, sops, age); the workspace (the credentials, the SSH key,
the GitOps repo and its urbex.platform.yaml); with a platform config and a
Proxmox token, the node (the API answers, the node, pool, storage, LXC
template and bridge exist, the token has the privileges urbex needs); and,
with Cloudflare in the config, the token and the zone.

Each check is ok, warn, fail or skip, with what to do when it isn't ok. It
exits non-zero when a check failed. It changes nothing, and prints no
secret. --output json prints the report as one JSON document. See
docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sys := doctor.LocalSystem()
			r := &doctor.Report{}
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
			var cf doctor.CloudflareReader
			if cfg != nil && creds.CloudflareToken != "" {
				cf = cloudflare.New(cloudflare.DefaultBaseURL, creds.CloudflareToken, cfg.Cloudflare.AccountID, cfg.Cloudflare.ZoneID)
			}
			doctor.CloudflareChecks(ctx, cf, cfg, r)

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
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	output.Register(cmd)
	return cmd
}

// printDoctor prints the report, a group at a time, with the fix under
// each check that isn't ok.
func printDoctor(out io.Writer, r *doctor.Report) {
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
