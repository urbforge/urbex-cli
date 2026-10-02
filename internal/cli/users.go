package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/provision"
)

func newUsersCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string

	cmd := &cobra.Command{
		Use:   "users <env>",
		Short: "Show an environment's Keycloak test users and their passwords",
		Long: `Each project environment has its own Keycloak realm, <project>-<env>,
with the roles the services declare in urbex.yaml (auth.roles). In
staging, urbex apply also creates a test user per role, test-<role>, with
a generated password kept SOPS-encrypted in the GitOps repo
(environments/staging/<project>/keycloak-users.sops.env). This prints
them, and how to get a token for one - through the realm's urbex-test
client, which allows password logins. Needs sops and URBEX_AGE_KEY.

See docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env := args[0]
			if !provision.ValidEnvs[env] {
				return fmt.Errorf("invalid environment %q: must be one of staging, prod", env)
			}
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}
			p, err := newProvisionOrchestrator(cmd, manifestPath, repoPath, "", false)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			users, err := p.TestUsers(cmd.Context(), repoPath, env)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Realm %s, issuer %s\n", p.Realm(env), p.Issuer(env))
			if len(users) == 0 {
				fmt.Fprintf(out, "No test users in %s: they are made in staging only, one per role declared in urbex.yaml (services[].auth.roles).\n", env)
				return nil
			}
			names := make([]string, 0, len(users))
			for name := range users {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(out, "  %-24s %s\n", name, users[name])
			}
			fmt.Fprintf(out, "\nA token for one of them:\n  curl -s %s/protocol/openid-connect/token -d grant_type=password -d client_id=%s \\\n    -d username=%s -d password=%s | jq -r .access_token\n",
				p.Issuer(env), provision.TestClient, names[0], users[names[0]])
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	return cmd
}
