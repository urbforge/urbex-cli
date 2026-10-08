package bootstrap

import (
	"github.com/urbforge/urbex-cli/internal/knownhosts"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type inventoryHost struct {
	AnsibleHost string `yaml:"ansible_host"`
	// Service labels the host's logs and metrics (gitea, komodo, ...).
	Service string `yaml:"urbex_service"`
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
	// GiteaURL and KomodoURL are the services' own addresses, by
	// internal name: Gitea's ROOT_URL, Komodo's KOMODO_HOST.
	GiteaURL            string `yaml:"gitea_url"`
	KomodoURL           string `yaml:"komodo_url"`
	GiteaAdminUsername  string `yaml:"gitea_admin_username"`
	KomodoAdminUsername string `yaml:"komodo_admin_username"`
	KomodoServerName    string `yaml:"komodo_server_name"`
	KomodoVersion       string `yaml:"komodo_version"`
	KomodoCoreAddress   string `yaml:"komodo_core_address"`
	// KeycloakPublicURL is Keycloak's public address, when it has one:
	// the issuer the apps' users see.
	KeycloakPublicURL string `yaml:"keycloak_public_url,omitempty"`
	// The telemetry agents' destinations and labels (role telemetry).
	LokiPushURL             string `yaml:"loki_push_url"`
	PrometheusWriteURL      string `yaml:"prometheus_write_url"`
	Kind                    string `yaml:"urbex_kind"`
	TelemetryComposeProject string `yaml:"telemetry_compose_project"`
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
// platform/ansible/playbook.yml), pointing at its statically allocated IP;
// ep are the endpoints the services are configured with, by internal
// name.
// Secrets are not inventory vars: roles read them from the environment.
func writeInventory(ansibleDir, domain string, ep Endpoints, pub PublicEndpoints, serviceIPs map[string]string) error {
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
				GiteaURL:             ep.GiteaURL,
				KomodoURL:            ep.KomodoURL,
				GiteaAdminUsername:   GiteaAdminUser,
				KomodoAdminUsername:  KomodoAdminUser,
				KomodoServerName:     KomodoServerName,
				KomodoVersion:        KomodoVersion,
				KomodoCoreAddress:    ep.KomodoURL,
				KeycloakPublicURL:    httpsURL(pub.Keycloak),
				LokiPushURL:          ep.LokiPushURL,
				PrometheusWriteURL:   ep.PrometheusWriteURL,
				Kind:                 "platform",
				// Every base service is a compose project in /opt/urbex.
				TelemetryComposeProject: "urbex",
			},
			Children: map[string]inventoryGroup{},
		},
	}

	for _, svc := range append(append([]Service{}, BaseServices...), TunnelService) {
		if serviceIPs[Hostname(svc.Name)] == "" {
			continue
		}
		hostname := Hostname(svc.Name)
		groupName := "urbex_" + svc.Name
		root.All.Children[groupName] = inventoryGroup{
			Hosts: map[string]inventoryHost{
				hostname: {AnsibleHost: serviceIPs[hostname], Service: svc.Name},
			},
		}
	}

	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(ansibleDir, "inventory.yml"), data, 0o644)
}

func httpsURL(host string) string {
	if host == "" {
		return ""
	}
	return "https://" + host
}
