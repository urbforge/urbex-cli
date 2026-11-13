package cli

import (
	"context"
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
	var output outputFlag
	var events eventsFlag

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

--output json prints the same as one JSON document, for a program to
read (a web UI, an MCP server, the Claude Code plugin); what couldn't be
checked is in its "warnings". It holds no password: the logins name the
keys of the credentials file.

With --progress, shows how the latest 'urbex bootstrap' or 'urbex apply'
run on this machine is getting on - from another terminal while it runs,
or afterwards: each phase with the time it took, the one running, those
left with an estimate (from the earlier runs, or the usual times), and
the time left. --watch refreshes it until the run ends. With --events
json it prints the run's events as JSON lines instead, from the first,
and with --watch follows it until it ends: how a program follows a run
again after a disconnect. See
docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if showProgressFlag || watch {
				if len(args) > 0 {
					return fmt.Errorf("--progress shows the latest bootstrap or apply: it takes no environment")
				}
				if events.value == "json" {
					return followEvents(cmd, cmd.OutOrStdout(), watch, time.Second)
				}
				if output.JSON() {
					return fmt.Errorf("--output json doesn't apply to --progress")
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
				if output.JSON() {
					st, err := platformStatus(cmd.Context(), client, cfg, creds, repoPath)
					if err != nil {
						return err
					}
					return writeJSON(out, st)
				}
				if err := runBaseStatus(cmd, out, client, cfg, creds); err != nil {
					return err
				}
				return printProjects(cmd.Context(), out, cfg, creds, repoPath)
			}
			if output.JSON() {
				var warnings []string
				st, err := projectStatus(cmd.Context(), func(w string) { warnings = append(warnings, w) }, client, cfg, creds, repoPath, manifestPath, args[0])
				if err != nil {
					return err
				}
				st.Warnings = warnings
				return writeJSON(out, st)
			}
			return runProjectStatus(cmd, out, client, cfg, creds, repoPath, manifestPath, args[0])
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest (only used when env is given)")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().BoolVar(&showProgressFlag, "progress", false, "show how the latest bootstrap or apply is getting on: phases, times, what is left")
	cmd.Flags().BoolVar(&watch, "watch", false, "with --progress: refresh every 2 seconds until the run ends")
	output.Register(cmd)
	cmd.Flags().Var(&events, "events", "with --progress: json prints the latest run's events as JSON lines (--watch follows them)")

	return cmd
}

// --- the platform ------------------------------------------------------------

// platformView is `urbex status --output json`: the platform, then its
// project environments.
type platformView struct {
	BaseServices []baseServiceView `json:"baseServices"`
	Endpoints    []endpointView    `json:"endpoints"`
	Logins       loginsView        `json:"logins"`
	Projects     []projectView     `json:"projects"`
	Warnings     []string          `json:"warnings,omitempty"`
}

type baseServiceView struct {
	Service  string `json:"service"`
	Hostname string `json:"hostname"`
	Present  bool   `json:"present"`
}

type endpointView struct {
	Service  string     `json:"service"`
	Name     string     `json:"name"`
	Internal string     `json:"internal"`
	LAN      string     `json:"lan"`
	Public   string     `json:"public,omitempty"`
	Note     string     `json:"note,omitempty"`
	DNS      *dnsRecord `json:"dns,omitempty"`
}

// loginsView names the platform's admin users and the keys of their
// passwords in the credentials file - never the passwords.
type loginsView struct {
	CredentialsFile string      `json:"credentialsFile"`
	Users           []loginView `json:"users"`
}

type loginView struct {
	Service     string `json:"service"`
	User        string `json:"user"`
	PasswordKey string `json:"passwordKey"`
}

func logins(path string) loginsView {
	return loginsView{CredentialsFile: path, Users: []loginView{
		{"gitea", bootstrap.GiteaAdminUser, "giteaAdminPassword"},
		{"komodo", bootstrap.GiteaAdminUser, "komodoAdminPassword"},
		{"grafana", "admin", "grafanaAdminPassword"},
		{"keycloak", "admin", "keycloakAdminPassword"},
		{"technitium", bootstrap.TechnitiumAdminUser, "technitiumAdminPassword"},
	}}
}

// baseStatus reads the base services' LXCs and the platform's endpoints,
// with their internal DNS records.
func baseStatus(ctx context.Context, client status.ProxmoxLister, cfg *platform.Config, creds *credentials.Credentials) ([]baseServiceView, []endpointView, error) {
	statuses, err := status.Base(ctx, client, cfg.Proxmox.Node, bootstrap.BaseServicesFor(cfg))
	if err != nil {
		return nil, nil, fmt.Errorf("checking Proxmox: %w", err)
	}
	services := make([]baseServiceView, 0, len(statuses))
	for _, s := range statuses {
		services = append(services, baseServiceView{Service: s.Service, Hostname: s.Hostname, Present: s.Present})
	}
	endpoints, err := bootstrap.PlatformEndpoints(cfg)
	if err != nil {
		return nil, nil, err
	}
	dns := newDNSChecker(cfg, creds)
	views := make([]endpointView, 0, len(endpoints))
	for _, e := range endpoints {
		internal, _ := url.Parse(e.Internal)
		lan, _ := url.Parse(e.LAN)
		views = append(views, endpointView{
			Service: e.Service, Name: e.Name, Internal: e.Internal, LAN: e.LAN, Public: e.Public, Note: e.Note,
			DNS: dns.Lookup(ctx, internal.Hostname(), lan.Hostname()),
		})
	}
	return services, views, nil
}

// platformStatus is everything `urbex status` shows, as data.
func platformStatus(ctx context.Context, client status.ProxmoxLister, cfg *platform.Config, creds *credentials.Credentials, repoPath string) (*platformView, error) {
	st := &platformView{}
	var err error
	if st.BaseServices, st.Endpoints, err = baseStatus(ctx, client, cfg, creds); err != nil {
		return nil, err
	}
	path, _ := credentials.DefaultPath()
	st.Logins = logins(credentials.HostPath(path))
	if st.Projects, err = projectsStatus(ctx, func(w string) { st.Warnings = append(st.Warnings, w) }, cfg, creds, repoPath); err != nil {
		return nil, err
	}
	return st, nil
}

func runBaseStatus(cmd *cobra.Command, out io.Writer, client status.ProxmoxLister, cfg *platform.Config, creds *credentials.Credentials) error {
	services, endpoints, err := baseStatus(cmd.Context(), client, cfg, creds)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Base services:")
	for _, s := range services {
		fmt.Fprintf(out, "  %-24s %s\n", s.Hostname, presence(s.Present))
	}
	// Each endpoint by internal name, LAN address and public URL; a
	// name whose record is missing or points elsewhere says so.
	fmt.Fprintln(out, "\nEndpoints:")
	for _, e := range endpoints {
		fmt.Fprintf(out, "  %-28s %-44s %-38s %s", e.Name, e.Internal, e.LAN, e.Public)
		if e.Note != "" {
			fmt.Fprintf(out, "  (%s)", e.Note)
		}
		if e.DNS != nil && e.DNS.State != dnsOK {
			fmt.Fprintf(out, "  %s", e.DNS)
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

// --- a project environment ---------------------------------------------------

// envView is `urbex status <env> --output json`.
type envView struct {
	Project    string            `json:"project"`
	Env        string            `json:"env"`
	Services   []envServiceView  `json:"services"`
	Reconciler *reconcilerView   `json:"reconciler,omitempty"`
	Endpoints  []envEndpointView `json:"endpoints"`
	Invalid    []string          `json:"invalid,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
	deployed   map[string]bool   // services Komodo reported on
	reconciler rollout.ReconcilerStatus
}

// envServiceView is one service of an environment. Hostname is its LXC,
// or a web frontend's Worker; Version, FollowsMain, Stack and Image are
// what the GitOps repo asks for and Komodo runs, when Komodo answered.
type envServiceView struct {
	Service     string `json:"service"`
	Hostname    string `json:"hostname"`
	Web         bool   `json:"web,omitempty"`
	Allocated   bool   `json:"allocated"`
	Provisioned bool   `json:"provisioned"`
	Version     string `json:"version,omitempty"`
	FollowsMain bool   `json:"followsMain,omitempty"`
	Stack       string `json:"stack,omitempty"`
	Image       string `json:"image,omitempty"`
}

type envEndpointView struct {
	Service string     `json:"service"`
	LAN     string     `json:"lan,omitempty"`
	Public  string     `json:"public,omitempty"`
	DNS     *dnsRecord `json:"dns,omitempty"`
}

// reconcilerView is what the reconciler did lately, for an environment
// whose applications use each other. State is running, stale or
// never-run.
type reconcilerView struct {
	State    string     `json:"state"`
	LastRun  *time.Time `json:"lastRun,omitempty"`
	Open     []string   `json:"open,omitempty"`
	Looping  []string   `json:"looping,omitempty"`
	Unstable []string   `json:"unstable,omitempty"`
}

func newReconcilerView(st rollout.ReconcilerStatus, now time.Time) *reconcilerView {
	if !st.Linked {
		return nil
	}
	v := &reconcilerView{State: "running", Open: st.Open, Looping: st.Looping, Unstable: st.Unstable}
	switch {
	case st.LastRun.IsZero():
		v.State = "never-run"
	case st.Stale(now):
		v.State = "stale"
	}
	if !st.LastRun.IsZero() {
		last := st.LastRun
		v.LastRun = &last
	}
	return v
}

// projectStatus reads an environment of the project in urbex.yaml; warn
// gets what couldn't be checked. A manifest that fails validation comes
// back with its errors in Invalid, and an error.
func projectStatus(ctx context.Context, warn func(string), client status.ProxmoxLister, cfg *platform.Config, creds *credentials.Credentials, repoPath, manifestPath, env string) (*envView, error) {
	if !provision.ValidEnvs[env] {
		return nil, fmt.Errorf("invalid environment %q: must be one of staging, prod", env)
	}
	refreshForStatus(ctx, warn, cfg, creds, repoPath)

	m, result, err := manifest.LoadTyped(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", manifestPath, err)
	}
	if m == nil {
		return &envView{Invalid: result.Errors}, fmt.Errorf("%s failed validation", manifestPath)
	}

	ledgerPath := filepath.Join(repoPath, "state", "allocations.json")
	ledger, err := allocation.Load(ledgerPath, cfg.Proxmox.VMIDBase+100, cfg.Proxmox.Network.BaseHostOffset+10)
	if err != nil {
		return nil, fmt.Errorf("loading allocation ledger: %w", err)
	}
	statuses, err := status.Project(ctx, client, cfg.Proxmox.Node, ledger, m.Project, env, m.Services)
	if err != nil {
		return nil, fmt.Errorf("checking Proxmox: %w", err)
	}

	// What the GitOps repo asks for and what Komodo runs, when this
	// machine has been bootstrapped; and what the reconciler says of the
	// environment, if its applications use each other.
	deployed := map[string]rollout.Deployed{}
	st := &envView{Project: m.Project, Env: env, deployed: map[string]bool{}}
	if len(creds.MissingForPlatform()) == 0 {
		ep, err := bootstrap.EndpointsFor(cfg)
		if err != nil {
			return nil, err
		}
		target := rollout.Target{Project: m.Project, Env: env, Services: m.Deployables()}
		r := newRollout(cfg, creds, ep, "", io.Discard)
		list, err := r.Status(ctx, target, envrepo.Repo{Path: repoPath})
		if err != nil {
			warn(fmt.Sprintf("could not query Komodo: %v", err))
		}
		for _, d := range list {
			deployed[d.Service] = d
		}
		if st.reconciler, err = r.ReconcilerStatus(ctx, m.Project, env); err != nil {
			warn(fmt.Sprintf("could not read the reconciler's state: %v", err))
		}
	}
	withDeployed := func(v envServiceView) envServiceView {
		if d, ok := deployed[v.Service]; ok {
			st.deployed[v.Service] = true
			v.Version, v.FollowsMain, v.Stack, v.Image = d.Want, d.Track, d.State, d.Image
		}
		return v
	}
	for _, s := range statuses {
		st.Services = append(st.Services, withDeployed(envServiceView{Service: s.Service, Hostname: s.Hostname, Allocated: s.Allocated, Provisioned: s.Provisioned}))
	}
	for _, svc := range m.Deployables() {
		if svc.IsWeb() {
			// No LXC: its Stack runs on the builder and deploys to a Worker.
			v := withDeployed(envServiceView{Service: svc.Name, Hostname: rollout.WorkerName(m.Project, env), Web: true})
			v.Image = ""
			st.Services = append(st.Services, v)
		}
	}
	st.Reconciler = newReconcilerView(st.reconciler, time.Now())

	// Where each service is reached: its LXC on the LAN and, if it is
	// public or a web frontend, its HTTPS address.
	o := &provision.Orchestrator{Manifest: m, Config: cfg}
	dns := newDNSChecker(cfg, creds)
	for _, svc := range m.Deployables() {
		e := envEndpointView{Service: svc.Name}
		if entry, ok := ledger.Lookup(provision.Hostname(m.Project, svc.Name, env)); ok && !svc.IsWeb() {
			e.LAN = provision.LANAddress(entry.IP, svc.Port)
			e.DNS = dns.Lookup(ctx, o.InternalHostname(env, svc), entry.IP)
		}
		if host := o.PublicHostname(env, svc); host != "" && (svc.Public || svc.IsWeb()) {
			e.Public = "https://" + host
		}
		st.Endpoints = append(st.Endpoints, e)
	}
	return st, nil
}

func runProjectStatus(cmd *cobra.Command, out io.Writer, client status.ProxmoxLister, cfg *platform.Config, creds *credentials.Credentials, repoPath, manifestPath, env string) error {
	warn := func(w string) { fmt.Fprintf(out, "(%s)\n", w) }
	st, err := projectStatus(cmd.Context(), warn, client, cfg, creds, repoPath, manifestPath, env)
	if st != nil && len(st.Invalid) > 0 {
		fmt.Fprintf(out, "%s failed validation (%d error(s)):\n", manifestPath, len(st.Invalid))
		for _, e := range st.Invalid {
			fmt.Fprintf(out, "  - %s\n", e)
		}
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "%s (%s):\n", st.Project, st.Env)
	for _, s := range st.Services {
		if s.Web {
			fmt.Fprintf(out, "  %-16s %-32s", s.Service, s.Hostname)
		} else {
			fmt.Fprintf(out, "  %-16s %-32s allocated=%-3s provisioned=%-3s",
				s.Service, s.Hostname, yesno(s.Allocated), yesno(s.Provisioned))
		}
		if st.deployed[s.Service] {
			fmt.Fprintf(out, " version=%s stack=%s", versionLabel(s.Version, s.FollowsMain), s.Stack)
			if s.Image != "" {
				fmt.Fprintf(out, " image=%s", s.Image)
			}
		}
		fmt.Fprintln(out)
	}
	printReconciler(out, st.reconciler, time.Now())
	fmt.Fprintln(out, "\nEndpoints:")
	for _, e := range st.Endpoints {
		lan := e.LAN
		if lan == "" {
			lan = "-"
		}
		fmt.Fprintf(out, "  %-16s %-28s %-48s %s\n", e.Service, lan, e.Public, e.DNS)
	}
	return nil
}

// versionLabel is a version as status prints it: "none" when the
// environment has none, "(follows-main)" after one that follows main.
func versionLabel(version string, track bool) string {
	if version == "" {
		version = "none"
	}
	if track {
		version += "(follows-main)"
	}
	return version
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
