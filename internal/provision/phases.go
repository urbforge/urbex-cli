package provision

import (
	"time"

	"github.com/urbforge/urbex-cli/internal/progress"
)

// The phases of urbex apply, as 'urbex status --progress' shows them
// (urbforge/urbex-cli#37) and docs/reference/startup.md in urbforge/urbex
// draws them.
const (
	PhasePrepare   = "prepare"
	PhaseLXCs      = "lxcs"
	PhaseConfigure = "configure"
	PhaseKomodo    = "komodo"
	PhaseAgents    = "agents"
	PhasePublish   = "publish"
	PhaseIdentity  = "identity"
	PhaseBuild     = "build"
	PhaseDeploy    = "deploy"
)

// Phases are apply's phases in order. Their estimates are the first apply
// of staging of the sample project on the test Proxmox node (2026-10-16:
// two services, main built for the first time); an environment's own
// runs replace them.
func Phases() []progress.PhaseSpec {
	return []progress.PhaseSpec{
		{ID: PhasePrepare, Title: "Check the platform, write the files", Estimate: 2 * time.Second},
		{ID: PhaseLXCs, Title: "Create or update the LXCs (Terraform)", Estimate: 15 * time.Second},
		{ID: PhaseConfigure, Title: "Configure the LXCs (Ansible)", Estimate: 20 * time.Second},
		{ID: PhaseKomodo, Title: "Set up the builds and the environment in Komodo", Estimate: 10 * time.Second},
		{ID: PhaseAgents, Title: "Deploy the telemetry agents", Estimate: 5 * time.Second},
		{ID: PhasePublish, Title: "Publish: tunnel, Access, internal DNS", Estimate: 5 * time.Second},
		{ID: PhaseIdentity, Title: "Keycloak: realm, clients, test users", Estimate: 5 * time.Second},
		{ID: PhaseBuild, Title: "Build main (Komodo)", Estimate: 250 * time.Second},
		{ID: PhaseDeploy, Title: "Deploy and wait for the services", Estimate: 30 * time.Second},
	}
}

// The phases of urbex destroy.
const (
	PhaseDestroyCheck    = "check"
	PhaseDestroyStacks   = "stacks"
	PhaseDestroyUnlink   = "unlink"
	PhaseDestroyMachines = "machines"
	PhaseDestroyServers  = "servers"
	PhaseDestroyGitOps   = "gitops"
)

// DestroyPhases are destroy's phases in order, for 'urbex status
// --progress' and --events json (ADR-0032).
func DestroyPhases() []progress.PhaseSpec {
	return []progress.PhaseSpec{
		{ID: PhaseDestroyCheck, Title: "Check what the environment has", Estimate: 2 * time.Second},
		{ID: PhaseDestroyStacks, Title: "Take the Stacks down (Komodo)", Estimate: 15 * time.Second},
		{ID: PhaseDestroyUnlink, Title: "Unpublish: tunnel, Access, internal DNS, Keycloak realm", Estimate: 5 * time.Second},
		{ID: PhaseDestroyMachines, Title: "Destroy the LXCs and VMs (Terraform)", Estimate: 15 * time.Second},
		{ID: PhaseDestroyServers, Title: "Remove the Komodo Servers and Builds", Estimate: 3 * time.Second},
		{ID: PhaseDestroyGitOps, Title: "Update the GitOps repo", Estimate: 3 * time.Second},
	}
}
