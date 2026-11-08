package cli

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
	"github.com/urbforge/urbex-cli/internal/rollout"
	"github.com/urbforge/urbex-cli/internal/status"
)

func newStatusCmd() *cobra.Command {
	var manifestPath string
	var gitopsRepoPath string
	var showProgressFlag, watch bool

	cmd := &cobra.Command{
		Use:   "status [env]",
		Short: "Show the current status of the platform or of a project environment",
		Long: `With no arguments, shows the platform and everything on it: which
base-service LXCs exist on Proxmox, every platform endpoint (on the LAN
and public) and where the logins are, then every project environment of
the GitOps repo - per service its version, what Komodo runs, and its LAN
and public addresses.

With an environment argument (staging or prod), shows, for each service
declared in urbex.yaml, whether urbex plan/apply has allocated it a
VMID/IP, whether its LXC currently exists on Proxmox, the version the
GitOps repo sets for it, and what Komodo reports it is running.

A version is a release (1.4.2) or a commit of main (a1b2c3d);
"(follows-main)" marks an environment that every push to main moves.

Each service with an LXC also shows its internal DNS name (Technitium),
read from the server: "(missing)" when it has no record, or where it
points when that is not the allocated IP. TLS certificates are not
tracked yet.

With --progress, shows how the latest 'urbex bootstrap' or 'urbex apply'
run on this machine is getting on - from another terminal while it runs,
or afterwards: each phase with the time it took, the one running, those
left with an estimate (from the earlier runs, or the usual times), and
the time left. --watch refreshes it until the run ends. See
docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if showProgressFlag || watch {
				if len(args) > 0 {
					return fmt.Errorf("--progress shows the latest bootstrap or apply: it takes no environment")
				}
				return showProgress(cmd, cmd.OutOrStdout(), watch, 2*time.Second)
			}
			repoPath, err := resolveGitOpsRepoPath(gitopsRepoPath)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			configPath := filepath.Join(repoPath, "urbex.platform.yaml")
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				return fmt.Errorf("%s not found - run 'urbex bootstrap --gitops-repo %s' first", configPath, repoPath)
			}
			cfg, err := platform.LoadFile(configPath)
			if err != nil {
				return fmt.Errorf("loading %s: %w", configPath, err)
			}

			creds, err := credentials.Load()
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
			if creds.ProxmoxToken == "" {
				return fmt.Errorf("URBEX_PROXMOX_TOKEN (or proxmoxToken in ~/.urbex/credentials.yaml) is required")
			}
			client := proxmoxapi.New(cfg.Proxmox.APIURL, creds.ProxmoxToken, cfg.Proxmox.Insecure)

			if len(args) == 0 {
				if err := runBaseStatus(cmd, out, client, cfg, creds); err != nil {
					return err
				}
				return printProjects(cmd.Context(), out, cfg, creds, repoPath)
			}
			return runProjectStatus(cmd, out, client, cfg, creds, repoPath, manifestPath, args[0])
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest (only used when env is given)")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().BoolVar(&showProgressFlag, "progress", false, "show how the latest bootstrap or apply is getting on: phases, times, what is left")
	cmd.Flags().BoolVar(&watch, "watch", false, "with --progress: refresh every 2 seconds until the run ends")

	return cmd
}

func runBaseStatus(cmd *cobra.Command, out io.Writer, client status.ProxmoxLister, cfg *platform.Config, creds *credentials.Credentials) error {
	statuses, err := status.Base(cmd.Context(), client, cfg.Proxmox.Node, bootstrap.BaseServicesFor(cfg))
	if err != nil {
		return fmt.Errorf("checking Proxmox: %w", err)
	}

	fmt.Fprintln(out, "Base services:")
	for _, s := range statuses {
		fmt.Fprintf(out, "  %-24s %s\n", s.Hostname, presence(s.Present))
	}

	endpoints, err := bootstrap.PlatformEndpoints(cfg)
	if err != nil {
		return err
	}
	// Each endpoint by internal name, LAN address and public URL; a
	// name whose record is missing or points elsewhere says so.
	dns := newDNSChecker(cfg, creds)
	fmt.Fprintln(out, "\nEndpoints:")
	for _, e := range endpoints {
		fmt.Fprintf(out, "  %-28s %-44s %-38s %s", e.Name, e.Internal, e.LAN, e.Public)
		if e.Note != "" {
			fmt.Fprintf(out, "  (%s)", e.Note)
		}
		if dns != nil {
			internal, _ := url.Parse(e.Internal)
			lan, _ := url.Parse(e.LAN)
			if record := dns.Check(cmd.Context(), internal.Hostname(), lan.Hostname()); strings.Contains(record, " (") {
				fmt.Fprintf(out, "  %s", record)
			}
		}
		fmt.Fprintln(out)
	}
	path, _ := credentials.DefaultPath()
	printLogins(out, credentials.HostPath(path))
	return nil
}

// printLogins lists the platform's admin users and the names of their
// passwords in the credentials file at path.
func printLogins(out io.Writer, path string) {
	fmt.Fprintf(out, "\nLogins: Gitea and Komodo %s, Grafana admin, Keycloak admin, Technitium %s; passwords in %s\n",
		bootstrap.GiteaAdminUser, bootstrap.TechnitiumAdminUser, path)
	fmt.Fprintln(out, "(giteaAdminPassword, komodoAdminPassword, grafanaAdminPassword, keycloakAdminPassword, technitiumAdminPassword).")
}

func runProjectStatus(cmd *cobra.Command, out io.Writer, client status.ProxmoxLister, cfg *platform.Config, creds *credentials.Credentials, repoPath, manifestPath, env string) error {
	if !provision.ValidEnvs[env] {
		return fmt.Errorf("invalid environment %q: must be one of staging, prod", env)
	}
	refreshForStatus(cmd.Context(), out, cfg, creds, repoPath)

	m, result, err := manifest.LoadTyped(manifestPath)
	if err != nil {
		return fmt.Errorf("loading %s: %w", manifestPath, err)
	}
	if m == nil {
		fmt.Fprintf(out, "%s failed validation (%d error(s)):\n", manifestPath, len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(out, "  - %s\n", e)
		}
		return fmt.Errorf("%s failed validation", manifestPath)
	}

	ledgerPath := filepath.Join(repoPath, "state", "allocations.json")
	ledger, err := allocation.Load(ledgerPath, cfg.Proxmox.VMIDBase+100, cfg.Proxmox.Network.BaseHostOffset+10)
	if err != nil {
		return fmt.Errorf("loading allocation ledger: %w", err)
	}

	statuses, err := status.Project(cmd.Context(), client, cfg.Proxmox.Node, ledger, m.Project, env, m.Services)
	if err != nil {
		return fmt.Errorf("checking Proxmox: %w", err)
	}

	// What the GitOps repo asks for and what Komodo runs, when this
	// machine has been bootstrapped; and what the reconciler says of the
	// environment, if its applications use each other.
	deployed := map[string]rollout.Deployed{}
	var reconciler rollout.ReconcilerStatus
	if len(creds.MissingForPlatform()) == 0 {
		ep, err := bootstrap.EndpointsFor(cfg)
		if err != nil {
			return err
		}
		target := rollout.Target{Project: m.Project, Env: env, Services: m.Deployables()}
		r := newRollout(cfg, creds, ep, "", out)
		list, err := r.Status(cmd.Context(), target, envrepo.Repo{Path: repoPath})
		if err != nil {
			fmt.Fprintf(out, "(could not query Komodo: %v)\n", err)
		}
		for _, d := range list {
			deployed[d.Service] = d
		}
		if reconciler, err = r.ReconcilerStatus(cmd.Context(), m.Project, env); err != nil {
			fmt.Fprintf(out, "(could not read the reconciler's state: %v)\n", err)
		}
	}

	fmt.Fprintf(out, "%s (%s):\n", m.Project, env)
	for _, s := range statuses {
		fmt.Fprintf(out, "  %-16s %-32s allocated=%-3s provisioned=%-3s",
			s.Service, s.Hostname, yesno(s.Allocated), yesno(s.Provisioned))
		if d, ok := deployed[s.Service]; ok {
			want := d.Want
			if want == "" {
				want = "none"
			}
			if d.Track {
				want += "(follows-main)"
			}
			fmt.Fprintf(out, " version=%s stack=%s", want, d.State)
			if d.Image != "" {
				fmt.Fprintf(out, " image=%s", d.Image)
			}
		}
		fmt.Fprintln(out)
	}
	for _, svc := range m.Deployables() {
		if !svc.IsWeb() {
			continue
		}
		// No LXC: its Stack runs on the builder and deploys to a Worker.
		fmt.Fprintf(out, "  %-16s %-32s", svc.Name, rollout.WorkerName(m.Project, env))
		if d, ok := deployed[svc.Name]; ok {
			want := d.Want
			if want == "" {
				want = "none"
			}
			if d.Track {
				want += "(follows-main)"
			}
			fmt.Fprintf(out, " version=%s stack=%s", want, d.State)
		}
		fmt.Fprintln(out)
	}
	printReconciler(out, reconciler, time.Now())
	// Where each service is reached: its LXC on the LAN and, if it is
	// public or a web frontend, its HTTPS address.
	o := &provision.Orchestrator{Manifest: m, Config: cfg}
	dns := newDNSChecker(cfg, creds)
	fmt.Fprintln(out, "\nEndpoints:")
	for _, svc := range m.Deployables() {
		lan, record := "-", ""
		if entry, ok := ledger.Lookup(provision.Hostname(m.Project, svc.Name, env)); ok && !svc.IsWeb() {
			lan = provision.LANAddress(entry.IP, svc.Port)
			record = dns.Check(cmd.Context(), o.InternalHostname(env, svc), entry.IP)
		}
		public := ""
		if host := o.PublicHostname(env, svc); host != "" && (svc.Public || svc.IsWeb()) {
			public = "https://" + host
		}
		fmt.Fprintf(out, "  %-16s %-28s %-48s %s\n", svc.Name, lan, public, record)
	}
	return nil
}

func presence(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

func yesno(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// printReconciler says what the reconciler (Komodo Action
// urbex-reconcile) did lately for an environment whose applications use
// each other: when it last ran - a dead man's switch, it runs every
// minute - and what it holds back.
func printReconciler(out io.Writer, st rollout.ReconcilerStatus, now time.Time) {
	if !st.Linked {
		return
	}
	switch {
	case st.LastRun.IsZero():
		fmt.Fprintf(out, "\nReconciler: has never run - is the Komodo Action %s scheduled?\n", bootstrap.ReconcileAction)
	case st.Stale(now):
		fmt.Fprintf(out, "\nReconciler: last heard from %s ago - not running: is the Komodo Action %s scheduled, and Komodo Core up?\n", now.Sub(st.LastRun).Round(time.Second), bootstrap.ReconcileAction)
	default:
		fmt.Fprintf(out, "\nReconciler: running, last heard from %s ago.\n", now.Sub(st.LastRun).Round(time.Second))
	}
	if len(st.Open) > 0 {
		fmt.Fprintf(out, "  circuit open, not deployed until it cools down: %s (see the Action's last runs in Komodo)\n", strings.Join(st.Open, ", "))
	}
	if len(st.Looping) > 0 {
		fmt.Fprintf(out, "  restarting (crash loop), what uses them waits: %s\n", strings.Join(st.Looping, ", "))
	}
	if len(st.Unstable) > 0 {
		fmt.Fprintf(out, "  restarted, what uses them waits until they stay up: %s\n", strings.Join(st.Unstable, ", "))
	}
}
