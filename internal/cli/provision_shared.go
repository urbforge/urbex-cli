package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/runner"
)

// resolveGitOpsRepoPath returns flagValue if set, else URBEX_GITOPS_REPO,
// else an error - the same convention used by `urbex bootstrap`.
func resolveGitOpsRepoPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("URBEX_GITOPS_REPO"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("--gitops-repo (or URBEX_GITOPS_REPO) is required")
}

// newProvisionOrchestrator loads and validates everything urbex
// plan/apply/deploy need: the local app manifest and the platform config
// from an already-bootstrapped GitOps repo, plus credentials when
// requireCreds is true (plan/apply need the Proxmox token for Terraform;
// deploy only runs Ansible over SSH and needs none). It writes validation
// errors to out itself (mirroring `urbex init`'s behavior) before
// returning a plain error.
func newProvisionOrchestrator(cmd *cobra.Command, manifestPath, gitopsRepoPath string, requireCreds bool) (*provision.Orchestrator, error) {
	out := cmd.OutOrStdout()

	m, result, err := manifest.LoadTyped(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", manifestPath, err)
	}
	if m == nil {
		fmt.Fprintf(out, "%s failed validation (%d error(s)):\n", manifestPath, len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(out, "  - %s\n", e)
		}
		return nil, fmt.Errorf("%s failed validation", manifestPath)
	}

	configPath := filepath.Join(gitopsRepoPath, "urbex.platform.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("%s not found - run 'urbex bootstrap --gitops-repo %s' first", configPath, gitopsRepoPath)
	}
	cfg, err := platform.LoadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", configPath, err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		fmt.Fprintf(out, "%s is invalid (%d error(s)):\n", configPath, len(errs))
		for _, e := range errs {
			fmt.Fprintf(out, "  - %s\n", e)
		}
		return nil, fmt.Errorf("%s failed validation", configPath)
	}

	creds, err := credentials.Load()
	if err != nil {
		return nil, fmt.Errorf("loading credentials: %w", err)
	}
	if requireCreds {
		if missing := creds.MissingForProvisioning(); len(missing) > 0 {
			fmt.Fprintln(out, "Missing required credentials:")
			for _, mk := range missing {
				fmt.Fprintf(out, "  - %s\n", mk)
			}
			return nil, fmt.Errorf("missing required credentials")
		}
	}

	return &provision.Orchestrator{
		Manifest:    m,
		Config:      cfg,
		Credentials: creds,
		Runner:      runner.ExecRunner{Stdout: out, Stderr: cmd.ErrOrStderr()},
		Out:         out,
	}, nil
}
