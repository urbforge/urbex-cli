package cli

import (
	"fmt"

	"github.com/spf13/cobra"

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

Komodo isn't yet watching tags to redeploy prod automatically, so
promote then immediately redeploys prod itself, the same way 'urbex
deploy prod' does (every declared service must already have been applied
to prod - see 'urbex apply prod'). Once Komodo integration exists, this
manual step becomes unnecessary; use --skip-deploy to only create/push
the tag today and preview that future behavior.`,
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
			o, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, false)
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
