package provision

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/bootstrap"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

type hostVars struct {
	AnsibleHost string `yaml:"ansible_host"`
}

type inventoryAll struct {
	Vars  map[string]string   `yaml:"vars"`
	Hosts map[string]hostVars `yaml:"hosts"`
}

type inventoryRoot struct {
	All inventoryAll `yaml:"all"`
}

// writeInventory renders an Ansible inventory for every service in the
// manifest, one host per <project>-<service>-<env> hostname, with what
// platform/ansible/roles/common and periphery need: the registry to
// trust and the Komodo Core to connect to. Nothing about the app itself -
// Komodo deploys that. The onboarding key is not an inventory var: the
// role reads it from the environment.
func writeInventory(ansibleDir, project, env string, services []manifest.Service, allocations map[string]allocation.Entry, ep bootstrap.Endpoints) error {
	root := inventoryRoot{
		All: inventoryAll{
			Vars: map[string]string{
				"ansible_user":            "root",
				"ansible_ssh_common_args": "-o StrictHostKeyChecking=accept-new",
				"gitea_registry":          ep.Registry,
				"komodo_core_address":     ep.KomodoURL,
				"komodo_version":          bootstrap.KomodoVersion,
			},
			Hosts: map[string]hostVars{},
		},
	}

	for _, svc := range services {
		hostname := Hostname(project, svc.Name, env)
		root.All.Hosts[hostname] = hostVars{AnsibleHost: allocations[hostname].IP}
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
