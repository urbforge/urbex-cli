package bootstrap

import (
	"github.com/urbforge/urbex-cli/internal/knownhosts"
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
	AnsibleTimeout       string `yaml:"ansible_timeout"`
	AnsibleSSHRetries    string `yaml:"ansible_ssh_retries"`
	UrbexDomain          string `yaml:"urbex_domain"`
	GiteaRegistry        string `yaml:"gitea_registry"`
	GiteaAdminUsername   string `yaml:"gitea_admin_username"`
	KomodoAdminUsername  string `yaml:"komodo_admin_username"`
	KomodoServerName     string `yaml:"komodo_server_name"`
	KomodoVersion        string `yaml:"komodo_version"`
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
// Secrets are not inventory vars: roles read them from the environment.
func writeInventory(ansibleDir, domain string, ep Endpoints, serviceIPs map[string]string) error {
	repoPath := filepath.Dir(ansibleDir)
	if err := knownhosts.Ensure(repoPath); err != nil {
		return err
	}
	sshArgs, err := knownhosts.SSHArgs(repoPath)
	if err != nil {
		return err
	}
	root := inventoryRoot{
		All: inventoryAll{
			Vars: inventoryVars{
				AnsibleUser:          "root",
				AnsibleSSHCommonArgs: sshArgs,
				AnsibleTimeout:       knownhosts.ConnectTimeout,
				AnsibleSSHRetries:    knownhosts.ConnectRetries,
				UrbexDomain:          domain,
				GiteaRegistry:        ep.Registry,
				GiteaAdminUsername:   GiteaAdminUser,
				KomodoAdminUsername:  KomodoAdminUser,
				KomodoServerName:     KomodoServerName,
				KomodoVersion:        KomodoVersion,
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
