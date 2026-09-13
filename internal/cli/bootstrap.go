package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

func newBootstrapCmd() *cobra.Command {
	var gitopsRepoPath string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Provision the base platform services on a fresh Proxmox server",
		Long: `Provisions the base-service LXCs (Gitea, Komodo, Technitium, Keycloak,
observability) on Proxmox via Terraform + Ansible, into the given GitOps
repo working directory.

If urbex.platform.yaml does not exist yet in that directory, bootstrap
scaffolds a starter one to be edited before running again.

Pushing the resulting GitOps repo to Gitea is a later phase, not yet
implemented - see docs/decisions/0006-bootstrap-command.md in
urbforge/urbex.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if gitopsRepoPath == "" {
				gitopsRepoPath = os.Getenv("URBEX_GITOPS_REPO")
			}
			if gitopsRepoPath == "" {
				return fmt.Errorf("--gitops-repo (or URBEX_GITOPS_REPO) is required")
			}
			return runBootstrap(cmd, gitopsRepoPath, dryRun)
		},
	}

	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "materialize assets and report the plan without running terraform/ansible-playbook")

	return cmd
}

func runBootstrap(cmd *cobra.Command, gitopsRepoPath string, dryRun bool) error {
	out := cmd.OutOrStdout()

	if err := os.MkdirAll(gitopsRepoPath, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", gitopsRepoPath, err)
	}

	configPath := gitopsRepoPath + "/urbex.platform.yaml"

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := platform.WriteTemplate(configPath); err != nil {
			return fmt.Errorf("scaffolding %s: %w", configPath, err)
		}
		fmt.Fprintf(out, "Created %s.\n", configPath)
		fmt.Fprintln(out, "Edit it with your Proxmox/domain details, then run 'urbex bootstrap' again.")
		return nil
	} else if err != nil {
		return fmt.Errorf("checking %s: %w", configPath, err)
	}

	cfg, err := platform.LoadFile(configPath)
	if err != nil {
		return fmt.Errorf("loading %s: %w", configPath, err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		fmt.Fprintf(out, "%s is invalid (%d error(s)):\n", configPath, len(errs))
		for _, e := range errs {
			fmt.Fprintf(out, "  - %s\n", e)
		}
		return fmt.Errorf("%s failed validation", configPath)
	}

	creds, err := credentials.Load()
	if err != nil {
		return fmt.Errorf("loading credentials: %w", err)
	}
	if missing := creds.MissingForProvisioning(); len(missing) > 0 {
		fmt.Fprintln(out, "Missing required credentials:")
		for _, m := range missing {
			fmt.Fprintf(out, "  - %s\n", m)
		}
		return fmt.Errorf("missing required credentials")
	}

	proxmoxClient := proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxToken)

	orchestrator := &bootstrap.Orchestrator{
		Config:      cfg,
		Credentials: creds,
		Proxmox:     proxmoxClient,
		Runner:      bootstrap.ExecRunner{Stdout: out, Stderr: cmd.ErrOrStderr()},
		Out:         out,
	}

	return orchestrator.Run(cmd.Context(), bootstrap.Options{
		GitOpsRepoPath: gitopsRepoPath,
		DryRun:         dryRun,
	})
}
