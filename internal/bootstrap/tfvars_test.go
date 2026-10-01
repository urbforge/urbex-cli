package bootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
)

// The LXCs copy the Proxmox host's resolv.conf unless nameservers are
// set - and an unset list must reach Terraform as [], never null.
func TestWriteTFVars_DNSServers(t *testing.T) {
	for _, tc := range []struct {
		servers []string
		want    string
	}{
		{nil, `[]`},
		{[]string{"192.168.1.1"}, `["192.168.1.1"]`},
	} {
		dir := t.TempDir()
		cfg := &platform.Config{}
		cfg.Proxmox.Network.DNSServers = tc.servers
		if err := writeTFVars(dir, cfg, map[string]string{}, 24); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "terraform.auto.tfvars.json"))
		if err != nil {
			t.Fatal(err)
		}
		var vars map[string]json.RawMessage
		if err := json.Unmarshal(data, &vars); err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := json.Compact(&got, vars["dns_servers"]); err != nil || got.String() != tc.want {
			t.Errorf("dns_servers = %s, want %s (%v)", got.String(), tc.want, err)
		}
	}
}
