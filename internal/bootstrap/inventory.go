package bootstrap

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type inventoryHost struct {
	AnsibleHost string `yaml:"ansible_host"`
}

type inventoryGroup struct {
	Hosts map[string]inventoryHost `yaml:"hosts"`
}

type inventoryVars struct {
	AnsibleUser          string `yaml:"ansible_user"`
	AnsibleSSHCommonArgs string `yaml:"ansible_ssh_common_args"`
}

type inventoryAll struct {
	Vars     inventoryVars             `yaml:"vars"`
	Children map[string]inventoryGroup `yaml:"children"`
}

type inventoryRoot struct {
	All inventoryAll `yaml:"all"`
}

// writeInventory renders an Ansible inventory grouping each base service
// under "urbex_<name>" (matching the hosts: fields in
// platform/ansible/playbook.yml), pointing at its statically allocated IP.
func writeInventory(ansibleDir string, serviceIPs map[string]string) error {
	root := inventoryRoot{
		All: inventoryAll{
			Vars: inventoryVars{
				AnsibleUser:          "root",
				AnsibleSSHCommonArgs: "-o StrictHostKeyChecking=accept-new",
			},
			Children: map[string]inventoryGroup{},
		},
	}

	for _, svc := range BaseServices {
		hostname := Hostname(svc.Name)
		groupName := "urbex_" + svc.Name
		root.All.Children[groupName] = inventoryGroup{
			Hosts: map[string]inventoryHost{
				hostname: {AnsibleHost: serviceIPs[hostname]},
			},
		}
	}

	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(ansibleDir, "inventory.yml"), data, 0o644)
}
