package bootstrap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/template"

	assets "github.com/urbforge/urbex-cli/platform"

	"github.com/urbforge/urbex-cli/internal/komodo"
	"github.com/urbforge/urbex-cli/internal/platform"
)

// AgentTag is the Komodo tag of the agents running next to the services:
// the telemetry agent's Stacks (ADR-0027).
const AgentTag = string(platform.KindAgent)

// TelemetryDir is the GitOps repo's folder of the telemetry agents' Stacks,
// one subfolder per LXC.
const TelemetryDir = PlatformStacksDir + "/telemetry"

// TelemetryAgent is the telemetry agent (Grafana Alloy) of one LXC: what
// it labels the LXC's own metrics with - its main service's kind,
// service and, for a project's, project and environment - and whether it
// ships the app's logs (a project's observability.logs).
type TelemetryAgent struct {
	Host               string
	Kind, Service      string
	Project, Env       string
	Logs               bool
	LokiPushURL        string
	PrometheusWriteURL string
	LXCLabels          []Label
	// ConfigHash is config.alloy's: a label of the container, so that a
	// changed configuration recreates it (a bind-mounted file stays as
	// the container first saw it).
	ConfigHash string
}

// Label is one name and value.
type Label struct{ Key, Value string }

// TelemetryStackName is the Komodo Stack of host's telemetry agent.
func TelemetryStackName(host string) string { return "urbex-telemetry-" + host }

var telemetryTemplates = template.Must(template.ParseFS(assets.Stacks,
	"stacks/telemetry/config.alloy.tmpl", "stacks/telemetry/compose.yaml.tmpl"))

// WriteTelemetryAgent writes host's telemetry Stack folder into the
// GitOps repo, its agent sending to ep's Loki and Prometheus.
func WriteTelemetryAgent(repoPath string, a TelemetryAgent, ep Endpoints) error {
	a.LokiPushURL, a.PrometheusWriteURL = ep.LokiPushURL, ep.PrometheusWriteURL
	a.LXCLabels = nil
	for k, v := range map[string]string{"kind": a.Kind, "service": a.Service, "project": a.Project, "env": a.Env} {
		if v != "" {
			a.LXCLabels = append(a.LXCLabels, Label{k, v})
		}
	}
	sort.Slice(a.LXCLabels, func(i, j int) bool { return a.LXCLabels[i].Key < a.LXCLabels[j].Key })
	dir := filepath.Join(repoPath, filepath.FromSlash(TelemetryDir), a.Host)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// config.alloy first: compose.yaml carries its hash.
	for _, f := range []struct{ file, tmpl string }{{"config.alloy", "config.alloy.tmpl"}, {"compose.yaml", "compose.yaml.tmpl"}} {
		var b bytes.Buffer
		if err := telemetryTemplates.ExecuteTemplate(&b, f.tmpl, a); err != nil {
			return fmt.Errorf("rendering %s's %s: %w", a.Host, f.file, err)
		}
		if f.file == "config.alloy" {
			sum := sha256.Sum256(b.Bytes())
			a.ConfigHash = hex.EncodeToString(sum[:])[:16]
		}
		if err := os.WriteFile(filepath.Join(dir, f.file), b.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RemoveTelemetryAgent deletes host's telemetry Stack folder.
func RemoveTelemetryAgent(repoPath, host string) error {
	return os.RemoveAll(filepath.Join(repoPath, filepath.FromSlash(TelemetryDir), host))
}

// TelemetryStackConfig is the Komodo Stack of host's telemetry agent:
// its folder of the GitOps repo, on host, in the compose project
// "telemetry" its agent had when Ansible ran it - adopting its data.
// A change to config.alloy redeploys it.
func TelemetryStackConfig(host string, internal Endpoints, org string) komodo.StackConfig {
	return komodo.StackConfig{
		ServerID:     host,
		GitProvider:  internal.Registry,
		GitAccount:   GiteaAdminUser,
		Repo:         org + "/" + GitOpsRepoName,
		Branch:       TrunkBranch,
		RunDirectory: TelemetryDir + "/" + host,
		ProjectName:  "telemetry",
		ConfigFiles:  []komodo.ConfigFile{{Path: "config.alloy", Requires: "Redeploy"}},
	}
}

// baseTelemetryAgents are the telemetry agents of the base services'
// LXCs: kind platform, their service, logs shipped.
func (o *Orchestrator) baseTelemetryAgents() []TelemetryAgent {
	var agents []TelemetryAgent
	for _, svc := range BaseServicesFor(o.Config) {
		agents = append(agents, TelemetryAgent{Host: Hostname(svc.Name), Kind: PlatformTag, Service: svc.Name, Logs: true})
	}
	return agents
}

// writeTelemetryAgents writes the base LXCs' telemetry Stack folders.
func (o *Orchestrator) writeTelemetryAgents(gitopsRepoPath string) error {
	internal, err := InternalEndpointsFor(o.Config)
	if err != nil {
		return err
	}
	for _, a := range o.baseTelemetryAgents() {
		if err := WriteTelemetryAgent(gitopsRepoPath, a, internal); err != nil {
			return err
		}
	}
	return nil
}

// telemetryStackDeploys are the base LXCs' telemetry agents, tagged
// agent.
func (o *Orchestrator) telemetryStackDeploys() ([]stackDeploy, error) {
	internal, err := InternalEndpointsFor(o.Config)
	if err != nil {
		return nil, err
	}
	var deploys []stackDeploy
	for _, a := range o.baseTelemetryAgents() {
		deploys = append(deploys, stackDeploy{name: TelemetryStackName(a.Host), tag: AgentTag,
			cfg: TelemetryStackConfig(a.Host, internal, o.Config.Git.Org)})
	}
	return deploys, nil
}
