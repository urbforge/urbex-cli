package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/gitea"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/rollout"
)

func newPromoteCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var merge bool

	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote staging to production",
		Long: `Opens the pull request that promotes staging to production on Gitea -
from the staging branch into main - or finds the one already open, and
prints its URL.

Merging that pull request is what deploys production: Komodo builds and
rolls out main on every push to it (see
docs/decisions/0019-branch-environments-komodo-rollouts.md in
urbforge/urbex). With --merge, promote merges it right away. Production
must already have been applied (see 'urbex apply prod').`,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			m, result, err := manifest.LoadTyped(manifestPath)
			if err != nil {
				return fmt.Errorf("loading %s: %w", manifestPath, err)
			}
			if m == nil {
				return fmt.Errorf("%s failed validation: %v", manifestPath, result.Errors)
			}
			cfg, err := platform.LoadFile(filepath.Join(repoPath, "urbex.platform.yaml"))
			if err != nil {
				return fmt.Errorf("loading platform config: %w", err)
			}
			creds, err := credentials.Load()
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
			if creds.GiteaToken == "" {
				return fmt.Errorf("URBEX_GITEA_TOKEN missing - run 'urbex bootstrap' on this machine first")
			}
			ep, err := bootstrap.EndpointsFor(cfg)
			if err != nil {
				return err
			}

			staging, prod := rollout.Branch("staging"), rollout.Branch("prod")
			g := gitea.New(ep.GiteaURL, creds.GiteaToken)
			ahead, err := g.CommitsAhead(cmd.Context(), cfg.Git.Org, m.Project, prod, staging)
			if err != nil {
				return err
			}
			if ahead == 0 {
				fmt.Fprintf(out, "Nothing to promote: %s has no commits that %s lacks.\n", staging, prod)
				return nil
			}
			pr, created, err := g.EnsurePullRequest(cmd.Context(), cfg.Git.Org, m.Project, staging, prod, "Promote staging to production")
			if err != nil {
				return err
			}
			if created {
				fmt.Fprintf(out, "Opened pull request #%d (%s -> %s): %s\n", pr.Number, staging, prod, pr.HTMLURL)
			} else {
				fmt.Fprintf(out, "Pull request #%d (%s -> %s) is already open: %s\n", pr.Number, staging, prod, pr.HTMLURL)
			}
			if !merge {
				fmt.Fprintln(out, "Merge it to deploy production (or re-run with --merge).")
				return nil
			}
			if err := g.MergePullRequest(cmd.Context(), cfg.Git.Org, m.Project, pr.Number); err != nil {
				return err
			}
			fmt.Fprintf(out, "Merged. Komodo is now building and deploying %s to prod; follow it with 'urbex status prod'.\n", prod)
			return nil
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().BoolVar(&merge, "merge", false, "merge the pull request right away, deploying production")
	return cmd
}
