package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/rollout"
)

// projectView is one project environment of the GitOps repo, in
// `urbex status --output json`.
type projectView struct {
	Project  string               `json:"project"`
	Env      string               `json:"env"`
	Services []projectServiceView `json:"services"`
}

// projectServiceView is a service of a project environment: its
// version, what Komodo runs (Stack, when Komodo answered), and where it
// is reached - its LXC on the LAN and, if published, its public address
// (from the tunnel's routes, a Quick Tunnel, or a web frontend's
// Worker).
type projectServiceView struct {
	Service     string     `json:"service"`
	Web         bool       `json:"web,omitempty"`
	LAN         string     `json:"lan,omitempty"`
	Public      string     `json:"public,omitempty"`
	Version     string     `json:"version,omitempty"`
	FollowsMain bool       `json:"followsMain,omitempty"`
	Stack       string     `json:"stack,omitempty"`
	DNS         *dnsRecord `json:"dns,omitempty"`
}

// projectsStatus reads every project environment in the GitOps repo;
// warn gets what couldn't be checked.
func projectsStatus(ctx context.Context, warn func(string), cfg *platform.Config, creds *credentials.Credentials, repoPath string) ([]projectView, error) {
	refreshForStatus(ctx, warn, cfg, creds, repoPath)
	repo := envrepo.Repo{Path: repoPath}
	deployments, err := repo.Deployments()
	if err != nil {
		return nil, err
	}
	views := []projectView{}
	if len(deployments) == 0 {
		return views, nil
	}

	ledger, err := allocation.Load(repoPath+"/state/allocations.json", 0, 0)
	if err != nil {
		return nil, err
	}
	public := publicRoutes(ctx, cfg, creds)
	dns := newDNSChecker(cfg, creds)

	var ro *rollout.Rollout
	if len(creds.MissingForPlatform()) == 0 {
		if ep, err := bootstrap.EndpointsFor(cfg); err == nil {
			ro = newRollout(cfg, creds, ep, "", io.Discard)
		}
	}

	for _, d := range deployments {
		view := projectView{Project: d.Project, Env: d.Env, Services: []projectServiceView{}}
		services := make([]manifest.Service, 0, len(d.Services))
		for _, name := range d.Services {
			// What the GitOps repo records, not the name, tells a web
			// frontend from a container called web.
			svc := manifest.Service{Name: name}
			if repo.WebFrontend(d.Env, d.Project, name) {
				svc.Runtime = manifest.WebRuntime
			}
			services = append(services, svc)
		}
		state := map[string]rollout.Deployed{}
		if ro != nil {
			list, err := ro.Status(ctx, rollout.Target{Project: d.Project, Env: d.Env, Services: services}, repo)
			if err == nil {
				for _, s := range list {
					state[s.Service] = s
				}
			}
		}
		o := &provision.Orchestrator{Manifest: &manifest.Manifest{Project: d.Project}, Config: cfg}
		for _, svc := range services {
			st, _ := repo.Version(d.Env, d.Project, svc.Name)
			v := projectServiceView{Service: svc.Name, Web: svc.IsWeb(), Version: st.Version, FollowsMain: st.Track}
			if svc.IsWeb() {
				if host := o.PublicHostname(d.Env, svc); host != "" {
					v.Public = "https://" + host
				}
			} else if entry, ok := ledger.Lookup(provision.Hostname(d.Project, svc.Name, d.Env)); ok {
				v.LAN = provision.LANAddress(entry.IP, repo.Port(d.Env, d.Project, svc.Name))
				v.DNS = dns.Lookup(ctx, o.InternalHostname(d.Env, svc), entry.IP)
				// Routes go to the service's internal name (ADR-0027);
				// ones set up before that, to its address.
				v.Public = public[o.InternalHostname(d.Env, svc)+":"+strconv.Itoa(repo.Port(d.Env, d.Project, svc.Name))]
				if v.Public == "" {
					v.Public = public[strings.TrimPrefix(v.LAN, "http://")]
				}
				if v.Public == "" && cfg.Cloudflare.QuickTunnel {
					v.Public = quickTunnelRoute(ctx, entry.IP)
				}
			}
			if s, ok := state[svc.Name]; ok {
				v.Stack = s.State
			}
			view.Services = append(view.Services, v)
		}
		views = append(views, view)
	}
	return views, nil
}

// printProjects lists every project environment in the GitOps repo: per
// service, the version it is set to, what Komodo runs, and where it is
// reached.
func printProjects(ctx context.Context, out io.Writer, cfg *platform.Config, creds *credentials.Credentials, repoPath string) error {
	views, err := projectsStatus(ctx, func(w string) { fmt.Fprintf(out, "(%s)\n", w) }, cfg, creds, repoPath)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "\nProjects:")
	if len(views) == 0 {
		fmt.Fprintln(out, "  none - 'urbex apply <env>' in a project creates one")
		return nil
	}
	for _, p := range views {
		fmt.Fprintf(out, "  %s (%s)\n", p.Project, p.Env)
		for _, v := range p.Services {
			lan := v.LAN
			if lan == "" {
				lan = "-"
			}
			stack := ""
			if v.Stack != "" {
				stack = " stack=" + v.Stack
			}
			fmt.Fprintf(out, "    %-14s %-28s %-48s version=%s%s", v.Service, lan, v.Public, versionLabel(v.Version, v.FollowsMain), stack)
			if v.DNS != nil {
				fmt.Fprintf(out, " %s", v.DNS)
			}
			fmt.Fprintln(out)
		}
	}
	return nil
}

// publicRoutes maps "ip:port" to the public address the platform's tunnel
// routes to it - empty without Cloudflare or its token.
func publicRoutes(ctx context.Context, cfg *platform.Config, creds *credentials.Credentials) map[string]string {
	routes := map[string]string{}
	if !cfg.Cloudflare.Enabled() || creds.CloudflareToken == "" {
		return routes
	}
	cf := cloudflare.New(cloudflare.DefaultBaseURL, creds.CloudflareToken, cfg.Cloudflare.AccountID, cfg.Cloudflare.ZoneID)
	tunnel, err := cf.FindTunnel(ctx, cfg.Cloudflare.TunnelName)
	if err != nil || tunnel == nil {
		return routes
	}
	list, err := cf.Routes(ctx, tunnel.ID)
	if err != nil {
		return routes
	}
	for _, r := range list {
		if r.Path == "" {
			routes[strings.TrimPrefix(r.Service, "http://")] = "https://" + r.Hostname
		}
	}
	return routes
}

// quickTunnelURL asks a service's LXC for its Quick Tunnel URL; tests
// replace it.
var quickTunnelURL = provision.QuickTunnelURL

// quickTunnelRoute is a service's Cloudflare Quick Tunnel URL (ADR-0025),
// or "" within a short timeout - most services have no sidecar to ask,
// and status must not hang on each of them in turn.
func quickTunnelRoute(ctx context.Context, ip string) string {
	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	url, err := quickTunnelURL(qctx, ip)
	if err != nil {
		return ""
	}
	return url
}

// refreshForStatus updates the GitOps repo from Gitea, where the release
// Action commits too, so what status reads is current; on failure it
// warns and goes on with the local copy.
func refreshForStatus(ctx context.Context, warn func(string), cfg *platform.Config, creds *credentials.Credentials, repoPath string) {
	if creds.GiteaToken == "" {
		return
	}
	ep, err := bootstrap.EndpointsFor(cfg)
	if err == nil {
		err = bootstrap.PullRepo(ctx, repoPath, ep.RepoURL(cfg.Git.Org, bootstrap.GitOpsRepoName), creds.GiteaToken)
	}
	if err != nil {
		warn(fmt.Sprintf("could not update the GitOps repo from Gitea: %v - showing the local copy", err))
	}
}
