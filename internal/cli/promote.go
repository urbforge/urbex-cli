package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/gitsync"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/promote"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/runner"
)

func newPromoteCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var repoDir string
	var remote string
	var version string
	var skipDeploy bool

	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote the current release from staging to production",
		Long: `Creates and pushes an annotated Git tag (vX.Y.Z, auto-incrementing the
patch version unless --version is given) at HEAD of the app repo - the
actual Git-based signal a production release happened (see
docs/decisions/0010-promotion-flow.md in urbforge/urbex).

The tag is also pushed to the app's repo on Gitea. Promote then deploys
that commit to prod the same way 'urbex deploy prod' does, reusing the
image already built for it (normally when it was deployed to staging) -
what reaches prod is exactly what was tested. Every declared service
must already have been applied to prod (see 'urbex apply prod'); use
--skip-deploy to only create/push the tag.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			tagger := &promote.Orchestrator{
				Runner: runner.ExecRunner{Stdout: out, Stderr: cmd.ErrOrStderr()},
				Out:    out,
			}

			tag, err := tagger.Tag(cmd.Context(), promote.Options{
				RepoDir: repoDir,
				Remote:  remote,
				Version: version,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Tagged and pushed %s.\n", tag)

			if skipDeploy {
				return nil
			}

			// deploy only runs Ansible over SSH - no Proxmox API token needed.
			o, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, repoDir, false)
			if err != nil {
				return err
			}

			if err := o.Deploy(cmd.Context(), provision.Options{Env: "prod", GitOpsRepoPath: repoPath}); err != nil {
				return fmt.Errorf("tag %s was pushed, but redeploying prod failed: %w", tag, err)
			}

			fmt.Fprintf(out, "Promoted %s to prod.\n", tag)
			return nil
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "path to the app's git repo")
	cmd.Flags().StringVar(&remote, "remote", "origin", "git remote to push the tag to")
	cmd.Flags().StringVar(&version, "version", "", "explicit version to tag (vX.Y.Z); default auto-increments the patch version")
	cmd.Flags().BoolVar(&skipDeploy, "skip-deploy", false, "only create/push the tag, don't redeploy prod")

	return cmd
}

// pushTagToGitea pushes tag to the app's repo on Gitea, which Komodo
// builds from, so the release is recorded where the images come from.
func pushTagToGitea(cmd *cobra.Command, gitopsRepoPath, manifestPath, repoDir, tag string) error {
	m, _, err := manifest.LoadTyped(manifestPath)
	if err != nil || m == nil {
		return fmt.Errorf("loading %s: %v", manifestPath, err)
	}
	cfg, err := platform.LoadFile(filepath.Join(gitopsRepoPath, "urbex.platform.yaml"))
	if err != nil {
		return err
	}
	creds, err := credentials.Load()
	if err != nil {
		return err
	}
	if creds.GiteaToken == "" {
		return fmt.Errorf("URBEX_GITEA_TOKEN missing - run 'urbex bootstrap' on this machine first")
	}
	ep, err := bootstrap.EndpointsFor(cfg)
	if err != nil {
		return err
	}
	return gitsync.Push(cmd.Context(), repoDir, ep.RepoURL(cfg.Git.Org, m.Project), creds.GiteaToken, "refs/tags/"+tag)
}
