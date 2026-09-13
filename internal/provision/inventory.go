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
// platform/ansible/roles/service.
func writeInventory(ansibleDir, project, env string, services []manifest.Service, allocations map[string]allocation.Entry) error {
	root := inventoryRoot{
		All: inventoryAll{
			Vars: map[string]string{
				"ansible_user":            "root",
				"ansible_ssh_common_args": "-o StrictHostKeyChecking=accept-new",
			},
			Hosts: map[string]hostVars{},
		},
	}

	for _, svc := range services {
		hostname := Hostname(project, svc.Name, env)
		entry := allocations[hostname]

		root.All.Hosts[hostname] = hostVars{
			AnsibleHost:     entry.IP,
			DockerImage:     placeholderImage(project, svc.Name, env),
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

// placeholderImage is a stand-in image reference. Actually building and
// pushing a real image is not yet implemented - see the TODO in
// platform/ansible/roles/service/templates/docker-compose.yml.j2.
func placeholderImage(project, service, env string) string {
	return project + "/" + service + ":" + env + "-latest"
}
