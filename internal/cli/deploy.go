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
	var followMain bool

	cmd := &cobra.Command{
		Use:   "deploy <env>",
		Short: "Set what an environment runs",
		Long: `Sets what the environment runs by writing it into each service's
version file in the GitOps repo
(environments/<env>/<project>/<service>/version.env) and pushing. Komodo
deploys from there; deploy then waits until the environment runs it.

  urbex deploy prod                    the project's latest release
  urbex deploy prod --version 1.4.2    a release - also how to roll back
  urbex deploy staging --version a1b2c3d   a commit of main (its image)
  urbex deploy staging --follow-main   follow main again: every push to
                                       main is deployed here

Deploying a version stops an environment from following main (staging
follows main by default); --follow-main resumes it.

Doing the same by hand, or through a pull request on the GitOps repo,
has the same effect: this command is a convenience. It also regenerates
the services' compose files from urbex.yaml, so it is how a changed port
or healthcheck takes effect.

Every declared service must already have been applied ('urbex apply').
See docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if followMain && version != "" {
				return fmt.Errorf("--version and --follow-main are exclusive")
			}
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			// deploy only talks to Gitea and Komodo - no Proxmox API token needed.
			p, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, "", false)
			if err != nil {
				return err
			}

			opts := provision.Options{Env: args[0], GitOpsRepoPath: repoPath, FollowMain: followMain}
			if !followMain {
				if version == "" {
					releases, err := p.rollout.Releases(cmd.Context(), p.Manifest.Project)
					if err != nil {
						return err
					}
					var names []string
					for _, r := range releases {
						names = append(names, r.Name)
					}
					latest, ok := semver.Latest(names)
					if !ok {
						return fmt.Errorf("%s has no release yet - run 'urbex release' first, or deploy a commit with --version", p.Manifest.Project)
					}
					version = latest.String()
				}
				opts.Version = version
			}
			return p.Deploy(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().StringVar(&version, "version", "", "release (X.Y.Z) or commit of main (short hash) to run; default is the latest release")
	cmd.Flags().BoolVar(&followMain, "follow-main", false, "make the environment follow main: deploy every push to main")
	return cmd
}
