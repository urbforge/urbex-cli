package provision

import (
	"github.com/urbforge/urbex-cli/internal/knownhosts"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

type hostVars struct {
	AnsibleHost string `yaml:"ansible_host"`
	// What the host's log agent labels its logs with.
	Project string `yaml:"urbex_project"`
	Service string `yaml:"urbex_service"`
	Env     string `yaml:"urbex_env"`
	// AppPort is only read by the quicktunnel role, on hosts in the
	// public_service group.
	AppPort string `yaml:"urbex_app_port,omitempty"`
}

type inventoryAll struct {
	Vars  map[string]string   `yaml:"vars"`
	Hosts map[string]hostVars `yaml:"hosts"`
}

// publicServiceGroup lists the hosts the quicktunnel role runs on: the
// manifest's public: true services, when cloudflare.quickTunnel is set
// and no Cloudflare account is configured (writeInventory's quickTunnel
// argument). Omitted entirely otherwise, which Ansible treats as an
// empty group - the role's play then matches nothing.
type publicServiceGroup struct {
	Hosts map[string]any `yaml:"hosts"`
}

type inventoryRoot struct {
	All           inventoryAll         `yaml:"all"`
	PublicService *publicServiceGroup  `yaml:"public_service,omitempty"`
}

// writeInventory renders an Ansible inventory for every service in the
// manifest, one host per <project>-<service>-<env> hostname, with what
// platform/ansible/roles/common and periphery need: the registry to
// trust and the Komodo Core to connect to. Nothing about the app itself
// otherwise - Komodo deploys that - except each public: true service's
// port, when quickTunnel is set: the quicktunnel role needs it to reach
// the app Komodo deploys later, on the same LXC. The onboarding key is
// not an inventory var: the role reads it from the environment.
func writeInventory(repoPath, ansibleDir, project, env string, services []manifest.Service, allocations map[string]allocation.Entry, ep bootstrap.Endpoints, shipLogs, quickTunnel bool) error {
	if err := knownhosts.Ensure(repoPath); err != nil {
		return err
	}
	sshArgs, err := knownhosts.SSHArgs(repoPath)
	if err != nil {
		return err
	}
	root := inventoryRoot{
		All: inventoryAll{
			Vars: map[string]string{
				"ansible_user":            "root",
				"ansible_ssh_common_args": sshArgs,
				"ansible_timeout":         knownhosts.ConnectTimeout,
				"ansible_ssh_retries":     knownhosts.ConnectRetries,
				"gitea_registry":          ep.Registry,
				"komodo_core_address":     ep.KomodoURL,
				"komodo_version":          bootstrap.KomodoVersion,
				// The services' logs go to the platform's Loki, unless the
				// manifest says observability.logs: false.
				"urbex_logs":           strconv.FormatBool(shipLogs && ep.LokiPushURL != ""),
				"loki_push_url":        ep.LokiPushURL,
				"prometheus_write_url": ep.PrometheusWriteURL,
				// A project's service, as opposed to the platform's.
				"urbex_kind": "app",
			},
			Hosts: map[string]hostVars{},
		},
	}

	var publicHosts map[string]any
	for _, svc := range services {
		hostname := Hostname(project, svc.Name, env)
		hv := hostVars{AnsibleHost: allocations[hostname].IP, Project: project, Service: svc.Name, Env: env}
		if quickTunnel && svc.Public {
			hv.AppPort = strconv.Itoa(svc.Port)
			if publicHosts == nil {
				publicHosts = map[string]any{}
			}
			publicHosts[hostname] = nil
		}
		root.All.Hosts[hostname] = hv
	}
	if publicHosts != nil {
		root.PublicService = &publicServiceGroup{Hosts: publicHosts}
	}

	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(ansibleDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ansibleDir, "inventory.yml"), data, 0o644)
}
