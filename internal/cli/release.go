package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/semver"
)

func newReleaseCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var version string

	cmd := &cobra.Command{
		Use:   "release",
		Short: "Release the project's main branch",
		Long: `Tags the head of the project's main branch on Gitea as the next release
(vX.Y.Z: the patch version after the latest release, v0.1.0 for the
first, or --version), then waits until Komodo has built every service's
image for it.

The tag is what Komodo builds from: pushing a vX.Y.Z tag yourself does
exactly the same. A release deploys nothing - 'urbex deploy <env>' (or a
commit to the GitOps repo) decides which release an environment runs.
See docs/decisions/0020-trunk-releases-gitops-environments.md in
urbforge/urbex.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			p, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, "", false)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			project := p.Manifest.Project

			tags, err := p.rollout.Tags(cmd.Context(), project)
			if err != nil {
				return err
			}
			next := semver.Next(tags)
			if version != "" {
				parsed, ok := semver.Parse(version)
				if !ok {
					return fmt.Errorf("invalid version %q: expected vX.Y.Z", version)
				}
				for _, t := range tags {
					if existing, ok := semver.Parse(t); ok && existing == parsed {
						return fmt.Errorf("release %s already exists", parsed.Tag())
					}
				}
				next = parsed
			}

			if err := p.rollout.Release(cmd.Context(), project, p.Manifest.Services, next); err != nil {
				return err
			}
			fmt.Fprintf(out, "Released %s %s:\n", project, next)
			for _, svc := range p.Manifest.Services {
				fmt.Fprintf(out, "  %-16s %s:%s\n", svc.Name, p.rollout.Image(project, svc.Name), next)
			}
			fmt.Fprintf(out, "Deploy it with 'urbex deploy staging' (add --version %s to be explicit).\n", next)
			return nil
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().StringVar(&version, "version", "", "version to release (vX.Y.Z); default is the next patch version")
	return cmd
}
