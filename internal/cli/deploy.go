package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/semver"
)

func newDeployCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var version string
	var followMain bool
	var app string
	var update bool
	var allowMajor bool
	var noSave bool

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

Applications from a published image (image and version in urbex.yaml)
run their tag's digest, pinned by the environment's first apply, until
it is changed:

  urbex deploy prod --update                the tags in urbex.yaml,
                                            resolved again, for all of them
  urbex deploy prod --app db --update       the same, for one
  urbex deploy prod --app db --version 17.7 another tag

--version also writes the tag into urbex.yaml, as npm install writes
package.json: the manifest follows the first environment of the stack -
staging, or prod if that is the only one - so that an environment created
again, or an update, starts from it. Deploying another environment, say
a rollback of prod, leaves it alone. Nothing is committed. --no-save
leaves urbex.yaml as it is.

A new major version of an application with volumes (17 to 18) is refused
without --allow-major: its data may need a dump and restore first.

Every declared service must already have been applied ('urbex apply').
See docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if followMain && version != "" {
				return fmt.Errorf("--version and --follow-main are exclusive")
			}
			if update && version != "" {
				return fmt.Errorf("--update and --version are exclusive")
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

			opts := provision.Options{Env: args[0], GitOpsRepoPath: repoPath, FollowMain: followMain,
				App: app, Update: update, AllowMajor: allowMajor}
			if app != "" || update {
				opts.Version = version
				if err := p.Deploy(cmd.Context(), opts); err != nil {
					return err
				}
				if version == "" || noSave {
					return nil
				}
				return saveVersion(cmd.OutOrStdout(), p.Manifest, manifestPath, args[0], app, version)
			}
			if !followMain && len(manifest.Built(p.Manifest.Deployables())) > 0 {
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
	cmd.Flags().StringVar(&app, "app", "", "the application from an image to deploy (with --update or --version <tag>)")
	cmd.Flags().BoolVar(&update, "update", false, "resolve the image tags in urbex.yaml again and run what they point to now")
	cmd.Flags().BoolVar(&allowMajor, "allow-major", false, "allow a new major version of an application from an image that has volumes")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "with --version for an application from an image, don't write the tag into urbex.yaml")
	return cmd
}

// saveVersion writes the tag an application was just deployed at into
// urbex.yaml - when the environment is the one the manifest follows (see
// Manifest.LeadingEnvironment) - so that the manifest and the GitOps repo
// don't drift apart. It never commits.
func saveVersion(out io.Writer, m *manifest.Manifest, path, env, app, version string) error {
	if lead := m.LeadingEnvironment(); env != lead {
		fmt.Fprintf(out, "urbex.yaml follows %s and is left as it is; --no-save is not needed here.\n", lead)
		return nil
	}
	svc, ok := m.Service(app)
	if !ok {
		return nil
	}
	changed, err := manifest.SetServiceVersion(path, app, version)
	if err != nil {
		return fmt.Errorf("deployed, but urbex.yaml was not updated: %w", err)
	}
	if changed {
		fmt.Fprintf(out, "urbex.yaml: %s version %s -> %s (not committed)\n", app, svc.Version, version)
	}
	return nil
}
