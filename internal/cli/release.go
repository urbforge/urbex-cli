package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/semver"
)

func newReleaseCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var version string
	var ref string

	cmd := &cobra.Command{
		Use:   "release",
		Short: "Release the commit staging runs (or main)",
		Long: `Tags a commit of the project's main branch on Gitea as the next release
(vX.Y.Z: the patch version after the latest release, v0.1.0 for the
first, or --version), then waits until its images are published.

Which commit: the one staging runs, if staging follows main - so the
release is exactly what was tested there - otherwise main's head, or
--ref (a branch, tag, or commit).

The tag is what makes the release: Komodo gives the commit's images,
which main already built, the version as tag - nothing is rebuilt.
Pushing a vX.Y.Z tag yourself does exactly the same. A release deploys
nothing: 'urbex promote' or 'urbex deploy prod' does. See
docs/reference/cli.md in urbforge/urbex.`,
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
			ctx := cmd.Context()
			project := p.Manifest.Project

			releases, err := p.rollout.Releases(ctx, project)
			if err != nil {
				return err
			}
			var names []string
			for _, r := range releases {
				names = append(names, r.Name)
			}
			next := semver.Next(names)
			if version != "" {
				parsed, ok := semver.Parse(version)
				if !ok {
					return fmt.Errorf("invalid version %q: expected vX.Y.Z", version)
				}
				for _, n := range names {
					if existing, _ := semver.Parse(n); existing == parsed {
						return fmt.Errorf("release %s already exists", parsed.Tag())
					}
				}
				next = parsed
			}

			target, why, err := releaseTarget(cmd, p, repoPath, ref)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Releasing %s %s from %s.\n", project, next.Tag(), why)
			if err := p.rollout.Release(ctx, project, p.Manifest.Deployables(), next, target); err != nil {
				return err
			}
			fmt.Fprintf(out, "Released %s %s:\n", project, next)
			for _, svc := range p.Manifest.Deployables() {
				fmt.Fprintf(out, "  %-16s %s:%s\n", svc.Name, p.rollout.Image(project, svc.Name), next)
			}
			fmt.Fprintln(out, "Put it in production with 'urbex promote' (or 'urbex deploy prod').")
			return nil
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().StringVar(&version, "version", "", "version to release (vX.Y.Z); default is the next patch version")
	cmd.Flags().StringVar(&ref, "ref", "", "commit, branch, or tag to release; default is what staging runs, else main")
	return cmd
}

// releaseTarget picks what a release tags: --ref; else the commit staging
// runs, if it follows main; else main's head. It returns a full commit
// hash or a branch name, and a description for the user.
func releaseTarget(cmd *cobra.Command, p *project, gitopsRepoPath, ref string) (string, string, error) {
	ctx := cmd.Context()
	if ref != "" {
		commit, err := p.rollout.ResolveCommit(ctx, p.Manifest.Project, ref)
		if err != nil {
			return "", "", err
		}
		return commit, fmt.Sprintf("%s (%s)", ref, commit[:7]), nil
	}
	if p.RefreshGitOps != nil {
		if err := p.RefreshGitOps(ctx, gitopsRepoPath); err != nil {
			return "", "", err
		}
	}
	repo := envrepo.Repo{Path: gitopsRepoPath}
	if repo.Exists("staging", p.Manifest.Project) && len(p.Manifest.Services) > 0 {
		st, err := repo.Version("staging", p.Manifest.Project, p.Manifest.Services[0].Name)
		if err != nil {
			return "", "", err
		}
		if _, isRelease := semver.Parse(st.Version); st.Track && st.Version != "" && !isRelease {
			commit, err := p.rollout.ResolveCommit(ctx, p.Manifest.Project, st.Version)
			if err != nil {
				return "", "", err
			}
			return commit, fmt.Sprintf("the commit staging runs (%s)", commit[:7]), nil
		}
	}
	commit, err := p.rollout.MainCommit(ctx, p.Manifest.Project)
	if err != nil {
		return "", "", err
	}
	return commit, fmt.Sprintf("main (%s)", commit[:7]), nil
}
