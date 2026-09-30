package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/semver"
)

func newDeployCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var version string

	cmd := &cobra.Command{
		Use:   "deploy <env>",
		Short: "Set the version an environment runs",
		Long: `Sets the environment to a release of the project - the latest one, or
--version - by writing it into each service's folder of the GitOps repo
(environments/<env>/<project>/<service>/version.env) and pushing. Komodo
deploys from there; deploy then waits until the environment runs it.

Doing the same by hand, or through a pull request on the GitOps repo,
has the same effect: this command is a convenience. It also regenerates
the services' compose files from urbex.yaml, so it is how a changed port
or healthcheck takes effect. Rolling back is deploying an older version.

Every declared service must already have been applied ('urbex apply').
See docs/decisions/0020-trunk-releases-gitops-environments.md in
urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			// deploy only talks to Gitea and Komodo - no Proxmox API token needed.
			p, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, "", false)
			if err != nil {
				return err
			}

			var v semver.Version
			if version != "" {
				parsed, ok := semver.Parse(version)
				if !ok {
					return fmt.Errorf("invalid version %q: expected X.Y.Z", version)
				}
				v = parsed
			} else {
				tags, err := p.rollout.Tags(cmd.Context(), p.Manifest.Project)
				if err != nil {
					return err
				}
				latest, ok := semver.Latest(tags)
				if !ok {
					return fmt.Errorf("%s has no release yet - run 'urbex release' first", p.Manifest.Project)
				}
				v = latest
			}

			return p.Deploy(cmd.Context(), provision.Options{
				Env:            args[0],
				GitOpsRepoPath: repoPath,
				Version:        v.String(),
			})
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().StringVar(&version, "version", "", "release to deploy (X.Y.Z); default is the project's latest release")
	return cmd
}
