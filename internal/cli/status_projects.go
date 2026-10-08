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

// printProjects lists every project environment in the GitOps repo: per
// service, the version it is set to, what Komodo runs, and where it is
// reached - its LXC on the LAN and, if published, its public address
// (from the tunnel's routes, or a web frontend's Worker).
func printProjects(ctx context.Context, out io.Writer, cfg *platform.Config, creds *credentials.Credentials, repoPath string) error {
	refreshForStatus(ctx, out, cfg, creds, repoPath)
	repo := envrepo.Repo{Path: repoPath}
	deployments, err := repo.Deployments()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "\nProjects:")
	if len(deployments) == 0 {
		fmt.Fprintln(out, "  none - 'urbex apply <env>' in a project creates one")
		return nil
	}

	ledger, err := allocation.Load(repoPath+"/state/allocations.json", 0, 0)
	if err != nil {
		return err
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
		fmt.Fprintf(out, "  %s (%s)\n", d.Project, d.Env)
		services := make([]manifest.Service, 0, len(d.Services))
		for _, name := range d.Services {
			svc := manifest.Service{Name: name}
			if name == manifest.WebService {
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
			version := st.Version
			if version == "" {
				version = "none"
			}
			if st.Track {
				version += "(follows-main)"
			}
			lan, pub, record := "-", "", ""
			if svc.IsWeb() {
				if host := o.PublicHostname(d.Env, svc); host != "" {
					pub = "https://" + host
				}
			} else if entry, ok := ledger.Lookup(provision.Hostname(d.Project, svc.Name, d.Env)); ok {
				lan = fmt.Sprintf("http://%s:%d", entry.IP, repo.Port(d.Env, d.Project, svc.Name))
				record = dns.Check(ctx, o.InternalHostname(d.Env, svc), entry.IP)
				// Routes go to the service's internal name (ADR-0027);
				// ones set up before that, to its address.
				pub = public[o.InternalHostname(d.Env, svc)+":"+strconv.Itoa(repo.Port(d.Env, d.Project, svc.Name))]
				if pub == "" {
					pub = public[strings.TrimPrefix(lan, "http://")]
				}
				if pub == "" && cfg.Cloudflare.QuickTunnel {
					pub = quickTunnelRoute(ctx, entry.IP)
				}
			}
			stack := ""
			if s, ok := state[svc.Name]; ok {
				stack = " stack=" + s.State
			}
			fmt.Fprintf(out, "    %-14s %-28s %-48s version=%s%s", svc.Name, lan, pub, version, stack)
			if record != "" {
				fmt.Fprintf(out, " %s", record)
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

// quickTunnelRoute is a service's Cloudflare Quick Tunnel URL (ADR-0025),
// or "" within a short timeout - most services have no sidecar to ask,
// and status must not hang on each of them in turn.
func quickTunnelRoute(ctx context.Context, ip string) string {
	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	url, err := provision.QuickTunnelURL(qctx, ip)
	if err != nil {
		return ""
	}
	return url
}

// refreshForStatus updates the GitOps repo from Gitea, where the release
// Action commits too, so what status reads is current; on failure it
// says so and goes on with the local copy.
func refreshForStatus(ctx context.Context, out io.Writer, cfg *platform.Config, creds *credentials.Credentials, repoPath string) {
	if creds.GiteaToken == "" {
		return
	}
	ep, err := bootstrap.EndpointsFor(cfg)
	if err == nil {
		err = bootstrap.PullRepo(ctx, repoPath, ep.RepoURL(cfg.Git.Org, bootstrap.GitOpsRepoName), creds.GiteaToken)
	}
	if err != nil {
		fmt.Fprintf(out, "(could not update the GitOps repo from Gitea: %v - showing the local copy)\n", err)
	}
}
