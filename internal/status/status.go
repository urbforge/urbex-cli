// Package status answers `urbex status`: what base/project-service LXCs
// exist on Proxmox, and (for project services) whether they've been
// allocated a VMID/IP by urbex plan/apply. DNS records, TLS certificates,
// and the latest deployed release are not tracked yet - see
// docs/architecture.md in urbforge/urbex.
package status

import (
	"context"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/provision"
)

// ProxmoxLister is the subset of proxmoxapi.Client this package needs.
type ProxmoxLister interface {
	ListContainerHostnames(ctx context.Context, node string) ([]string, error)
}

// BaseServiceStatus is whether one base-service LXC exists on Proxmox.
type BaseServiceStatus struct {
	Service  string
	Hostname string
	Present  bool
}

// Base reports the presence of every base-service LXC (see
// internal/bootstrap.BaseServices) on the given Proxmox node.
func Base(ctx context.Context, proxmox ProxmoxLister, node string, services []bootstrap.Service) ([]BaseServiceStatus, error) {
	existing, err := proxmox.ListContainerHostnames(ctx, node)
	if err != nil {
		return nil, err
	}
	present := toSet(existing)

	statuses := make([]BaseServiceStatus, 0, len(services))
	for _, svc := range services {
		hostname := bootstrap.Hostname(svc.Name)
		statuses = append(statuses, BaseServiceStatus{
			Service:  svc.Name,
			Hostname: hostname,
			Present:  present[hostname],
		})
	}
	return statuses, nil
}

// ServiceStatus is one project service's provisioning status for one
// environment.
type ServiceStatus struct {
	Service     string
	Hostname    string
	Allocated   bool // has a VMID/IP been assigned (urbex plan/apply ran)
	Provisioned bool // does the LXC currently exist on Proxmox
}

// Project reports, for each service in a manifest, whether it has an
// allocation in ledger and whether its LXC exists on the given Proxmox
// node.
func Project(ctx context.Context, proxmox ProxmoxLister, node string, ledger *allocation.Ledger, project, env string, services []manifest.Service) ([]ServiceStatus, error) {
	existing, err := proxmox.ListContainerHostnames(ctx, node)
	if err != nil {
		return nil, err
	}
	present := toSet(existing)

	statuses := make([]ServiceStatus, 0, len(services))
	for _, svc := range services {
		hostname := provision.Hostname(project, svc.Name, env)
		_, allocated := ledger.Lookup(hostname)
		statuses = append(statuses, ServiceStatus{
			Service:     svc.Name,
			Hostname:    hostname,
			Allocated:   allocated,
			Provisioned: present[hostname],
		})
	}
	return statuses, nil
}

func toSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, i := range items {
		set[i] = true
	}
	return set
}
