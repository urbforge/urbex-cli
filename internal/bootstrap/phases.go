package bootstrap

import (
	"time"

	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/progress"
)

// The phases of urbex bootstrap, as 'urbex status --progress' shows them
// (urbforge/urbex-cli#37) and docs/reference/startup.md in urbforge/urbex
// draws them.
const (
	PhasePrepare    = "prepare"
	PhaseTemplate   = "template"
	PhaseLXCs       = "lxcs"
	PhaseCommon     = "common"
	PhaseTechnitium = "technitium"
	PhaseBase       = "base"
	PhaseAgents     = "agents"
	PhaseDNS        = "dns"
	PhaseGitOps     = "gitops"
	PhaseConnect    = "connect"
	PhaseStacks     = "stacks"
	PhaseCloudflare = "cloudflare"
)

// Phases are bootstrap's phases in order. Their estimates are a bootstrap
// from an empty node on the test Proxmox node (2026-10-16); the runs on a
// platform replace them with what its own runs took. Ansible's plays
// (platform/ansible/playbook.yml) begin theirs.
func Phases(cfg *platform.Config) []progress.PhaseSpec {
	return []progress.PhaseSpec{
		{ID: PhasePrepare, Title: "Check Proxmox, write the GitOps repo's files", Estimate: 5 * time.Second},
		{ID: PhaseTemplate, Title: "Build the LXC template", Estimate: 190 * time.Second},
		{ID: PhaseLXCs, Title: "Create the LXCs (Terraform)", Estimate: 50 * time.Second},
		{ID: PhaseCommon, Title: "Configure every LXC (Ansible)", Estimate: 20 * time.Second, Play: "Configure all urbex base-service containers"},
		{ID: PhaseTechnitium, Title: "Install Technitium (DNS)", Estimate: 25 * time.Second, Play: "Deploy Technitium DNS"},
		{ID: PhaseBase, Title: "Install Gitea and Komodo, pull the other images", Estimate: 290 * time.Second, Play: "Deploy the other base services in parallel"},
		{ID: PhaseAgents, Title: "Prepare the telemetry agents", Estimate: 20 * time.Second, Play: "Send every base service's logs and resource metrics to the observability stack"},
		{ID: PhaseDNS, Title: "Configure the internal DNS", Estimate: 10 * time.Second},
		{ID: PhaseGitOps, Title: "Set up Gitea, Komodo and the GitOps repo", Estimate: 20 * time.Second},
		{ID: PhaseConnect, Title: "Connect the LXCs to Komodo", Estimate: 20 * time.Second, Play: "Connect the base services' LXCs to Komodo"},
		{ID: PhaseStacks, Title: "Deploy the platform's Stacks (Komodo)", Estimate: 90 * time.Second},
		{ID: PhaseCloudflare, Title: "Publish on Cloudflare", Estimate: 30 * time.Second},
	}
}
