package proxmoxapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// What urbex doctor and discover read, as Proxmox VE 9.2 answers a
// pool-scoped token (from the Cubotto lab).
func TestInspect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			w.Write([]byte(`{"data":{"release":"9.2","version":"9.2.21"}}`))
		case "/nodes":
			w.Write([]byte(`{"data":[{"node":"cubotto","status":"online"}]}`))
		case "/pools":
			w.Write([]byte(`{"data":[{"poolid":"urbex","comment":"x"}]}`))
		case "/access/permissions":
			w.Write([]byte(`{"data":{"/pool/urbex":{"VM.Allocate":0},"/sdn/zones/localnetwork/vmbr0":{"SDN.Use":0}}}`))
		case "/nodes/cubotto/storage":
			w.Write([]byte(`{"data":[{"storage":"local-zfs","type":"zfspool","content":"images,rootdir","avail":946868350976,"total":962193256448,"active":1},{"storage":"local","type":"dir","content":"vztmpl,import,backup,iso","active":1}]}`))
		case "/nodes/cubotto/storage/local/content":
			if r.URL.Query().Get("content") != "vztmpl" {
				t.Errorf("content = %s", r.URL.Query().Get("content"))
			}
			w.Write([]byte(`{"data":[{"volid":"local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst"}]}`))
		case "/nodes/cubotto/network":
			w.Write([]byte(`{"data":[{"iface":"vmbr0","cidr":"192.168.1.33/24","gateway":"192.168.1.1","type":"bridge"}]}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c, ctx := New(server.URL, "t", false), context.Background()

	if v, err := c.Version(ctx); err != nil || v != "9.2.21" {
		t.Errorf("Version = %q, %v", v, err)
	}
	if n, _ := c.Nodes(ctx); !reflect.DeepEqual(n, []string{"cubotto"}) {
		t.Errorf("Nodes = %v", n)
	}
	if p, _ := c.Pools(ctx); !reflect.DeepEqual(p, []string{"urbex"}) {
		t.Errorf("Pools = %v", p)
	}
	if perms, _ := c.Permissions(ctx); !perms["/pool/urbex"]["VM.Allocate"] || !perms["/sdn/zones/localnetwork/vmbr0"]["SDN.Use"] {
		t.Errorf("Permissions = %v", perms)
	}
	s, err := c.Storages(ctx, "cubotto")
	if err != nil || len(s) != 2 || s[0].Name != "local" || !s[1].Has("rootdir") || s[1].AvailGB != 881 || !s[1].Active {
		t.Errorf("Storages = %+v, %v", s, err)
	}
	if v, _ := c.Volumes(ctx, "cubotto", "local", "vztmpl"); !reflect.DeepEqual(v, []string{"local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst"}) {
		t.Errorf("Volumes = %v", v)
	}
	if b, _ := c.Bridges(ctx, "cubotto"); !reflect.DeepEqual(b, []Bridge{{Name: "vmbr0", CIDR: "192.168.1.33/24", Gateway: "192.168.1.1"}}) {
		t.Errorf("Bridges = %+v", b)
	}
}
