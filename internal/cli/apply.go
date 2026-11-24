package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/projectrepo"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/runner"
)

func newApplyCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var deleteData bool
	var repoDir string
	var events eventsFlag

	cmd := &cobra.Command{
		Use:   "apply <env>",
		Short: "Apply the planned infrastructure changes for an environment",
		Long: `Like 'urbex plan', but runs 'terraform apply', then installs Docker,
the Komodo Periphery agent, and sops on each service's LXC, and sets up:

  - the project: its repo on Gitea (creating main from the local HEAD of
    --repo-dir if Gitea doesn't have it), a Komodo Build per service,
    and the webhook that has Komodo build every push to main and publish
    every release tag (vX.Y.Z);
  - the environment: each service's folder in the GitOps repo
    (environments/<env>/<project>/<service>/: compose file,
    configuration, secrets, version) and the Komodo Stack that deploys
    it.

Staging follows main: apply builds main's head and deploys it, and every
later push to main is deployed there too. Production starts empty until
'urbex promote' (or 'urbex deploy prod') gives it a release.
Re-applying an environment that runs something redeploys it.

It also keeps the environment's Keycloak realm (<project>-<env>): the
roles the services declare, a client per frontend and, in staging, a
test user per role ('urbex users staging'). It registers every
service's internal DNS name in Technitium, independently of Cloudflare,
and, with Cloudflare, publishes the services marked public: true.
Addresses are printed at the end.

Each named volume becomes a mount point of its application's LXC,
included in Proxmox's backups, and grows when its size does; a change
that would replace an LXC with named volumes is refused, and so is
removing an application that has them from urbex.yaml, unless
--delete-data says to delete it with its volumes. Shares are
bound by the operator: until an application's are, apply prints the
pct commands, deploys the rest, and fails.

urbex.yaml must be committed in the git repository at --repo-dir until
the project has its main branch on Forgejo: apply checks it before
creating anything.

Requires urbex.platform.yaml to already exist in --gitops-repo (created
by 'urbex bootstrap'). See docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			ev, err := events.begin(cmd, "apply")
			if err != nil {
				return err
			}
			defer func() { ev.close(err) }()

			o, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, repoDir, true)
			if err != nil {
				return err
			}
			// Before anything is created: the project's repo on Forgejo
			// comes from urbex.yaml's git repository.
			if err := checkProjectRepo(cmd, o, repoDir, manifestPath); err != nil {
				return err
			}

			tracker := startProgress(cmd, "apply", fmt.Sprintf("urbex apply %s (%s)", args[0], o.Manifest.Project), provision.Phases(), ev)
			o.Progress = tracker
			// Terraform's and Ansible's output records the steps.
			o.Runner = runner.ExecRunner{Stdout: tracker.Watch(cmd.OutOrStdout()), Stderr: cmd.ErrOrStderr()}
			err = o.Apply(cmd.Context(), provision.Options{
				Env:            args[0],
				GitOpsRepoPath: repoPath,
				DeleteData:     deleteData,
			})
			tracker.Finish(err)
			return err
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().StringVar(&repoDir, "repo-dir", ".", "path to the app's git repo, used to create the project's main branch on Forgejo if it is missing")

	cmd.Flags().BoolVar(&deleteData, "delete-data", false, "delete the LXC and the named volumes of an application removed from urbex.yaml")
	events.Register(cmd)

	return cmd
}

// checkProjectRepo refuses a project whose urbex.yaml isn't committed in
// the git repository at repoDir, unless Forgejo already has the
// project's main branch: apply would otherwise create the machines and
// stop when it pushes the branch.
func checkProjectRepo(cmd *cobra.Command, o *project, repoDir, manifestPath string) error {
	local := projectrepo.Check(cmd.Context(), repoDir, manifestPath)
	if local == nil || (o.rollout != nil && !o.rollout.NeedsLocalTrunk(cmd.Context(), o.Manifest.Project)) {
		return nil
	}
	return local
}
