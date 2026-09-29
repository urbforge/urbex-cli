package provision

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

type hostVars struct {
	AnsibleHost     string            `yaml:"ansible_host"`
	DockerImage     string            `yaml:"docker_image"`
	ContainerPort   int               `yaml:"container_port"`
	HealthcheckPath string            `yaml:"healthcheck_path,omitempty"`
	EnvVars         map[string]string `yaml:"env_vars,omitempty"`
}

type inventoryAll struct {
	Vars  map[string]string   `yaml:"vars"`
	Hosts map[string]hostVars `yaml:"hosts"`
}

type inventoryRoot struct {
	All inventoryAll `yaml:"all"`
}

// writeInventory renders an Ansible inventory for every service in the
// manifest, one host per <project>-<service>-<env> hostname, with the
// per-service docker-compose template variables consumed by
// platform/ansible/roles/service. images maps service name to the image
// reference to run; it is committed to the GitOps repo, so the inventory
// is also the record of what each environment runs. The registry pull
// token is not an inventory var: the role reads it from the environment.
func writeInventory(ansibleDir, project, env string, services []manifest.Service, allocations map[string]allocation.Entry, images map[string]string, registry, registryUser string) error {
	root := inventoryRoot{
		All: inventoryAll{
			Vars: map[string]string{
				"ansible_user":            "root",
				"ansible_ssh_common_args": "-o StrictHostKeyChecking=accept-new",
				"gitea_registry":          registry,
				"registry_username":       registryUser,
			},
			Hosts: map[string]hostVars{},
		},
	}

	for _, svc := range services {
		hostname := Hostname(project, svc.Name, env)
		entry := allocations[hostname]

		root.All.Hosts[hostname] = hostVars{
			AnsibleHost:     entry.IP,
			DockerImage:     images[svc.Name],
			ContainerPort:   svc.Port,
			HealthcheckPath: svc.Healthcheck,
			EnvVars:         svc.EnvFor(env),
		}
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
