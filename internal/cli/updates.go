package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newUpdatesCmd() *cobra.Command {
	var manifestPath, gitopsRepoPath string
	var output outputFlag

	cmd := &cobra.Command{
		Use:   "updates [<env>]",
		Short: "List newer images of the project's applications from published images",
		Long: `For each application from a published image applied in <env> (or in
staging and prod), says whether a newer image is available:

  - the tag urbex.yaml asks for now points to another image than the one
    running: 'urbex deploy <env> --update' moves to it;
  - a newer tag of the same major (a minor or patch version): deploy it
    with 'urbex deploy <env> --app <app> --version <tag>', and set it in
    urbex.yaml;
  - a newer major: listed only - its data may need a migration, and its
    release notes say how. urbex never moves an application to it by
    itself.

Only tags shaped like the one urbex.yaml asks for are compared: "1.29"
with "1.30" and "2.0", not with "1.30.1" or "latest". It changes nothing.
--output json prints the list as one JSON document. See
docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			p, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, "", false)
			if err != nil {
				return err
			}
			opts := provision.Options{GitOpsRepoPath: repoPath}
			if len(args) == 1 {
				opts.Env = args[0]
			}
			updates, err := p.Updates(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if output.JSON() {
				if updates == nil {
					updates = []provision.ImageUpdate{}
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{"project": p.Manifest.Project, "updates": updates})
			}
			printUpdates(cmd.OutOrStdout(), p.Manifest.Project, updates)
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	output.Register(cmd)
	return cmd
}

func printUpdates(out io.Writer, project string, updates []provision.ImageUpdate) {
	if len(updates) == 0 {
		fmt.Fprintf(out, "%s: no application from a published image is applied yet.\n", project)
		return
	}
	env := ""
	for _, u := range updates {
		if u.Env != env {
			env = u.Env
			fmt.Fprintf(out, "%s (%s):\n", project, env)
		}
		fmt.Fprintf(out, "  %-16s %s:%s, running %s\n", u.App, u.Image, u.Asked, u.Running)
		if u.Error != "" {
			fmt.Fprintf(out, "  %-16s couldn't check: %s\n", "", u.Error)
		}
		none := true
		if u.NewDigest {
			none = false
			fmt.Fprintf(out, "  %-16s %s points to a newer image: urbex deploy %s --update\n", "", u.Asked, u.Env)
		}
		if u.Minor != "" {
			none = false
			fmt.Fprintf(out, "  %-16s newer: %s - urbex deploy %s --app %s --version %s\n", "", u.Minor, u.Env, u.App, u.Minor)
		}
		if u.Major != "" {
			none = false
			fmt.Fprintf(out, "  %-16s new major: %s - read its release notes first; not proposed\n", "", u.Major)
		}
		if none && u.Error == "" {
			fmt.Fprintf(out, "  %-16s up to date\n", "")
		}
	}
}
