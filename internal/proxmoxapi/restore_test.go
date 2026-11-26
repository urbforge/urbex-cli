package proxmoxapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestMachines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cluster/resources" || r.URL.Query().Get("type") != "vm" {
			t.Errorf("unexpected %s", r.URL)
		}
		w.Write([]byte(`{"data":[
			{"vmid":9000,"name":"urbex-gitea","type":"lxc","node":"pve","pool":"urbex","status":"running","template":0},
			{"vmid":"9090","name":"urbex-template","type":"lxc","node":"pve","pool":"urbex","status":"stopped","template":1},
			{"vmid":9180,"name":"app-vm-prod","type":"qemu","node":"pve","status":"stopped"}]}`))
	}))
	defer server.Close()

	got, err := New(server.URL, "t", false).Machines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Machine{
		{VMID: 9000, Name: "urbex-gitea", Kind: "lxc", Node: "pve", Pool: "urbex", Status: "running"},
		{VMID: 9090, Name: "urbex-template", Kind: "lxc", Node: "pve", Pool: "urbex", Status: "stopped", Template: true},
		{VMID: 9180, Name: "app-vm-prod", Kind: "qemu", Node: "pve", Status: "stopped"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("machine %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Backups come oldest first, and only the asked machine's, whatever the
// API returns.
func TestBackups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/nodes/pve/storage/pbs/content" || q.Get("content") != "backup" || q.Get("vmid") != "9000" {
			t.Errorf("unexpected %s", r.URL)
		}
		w.Write([]byte(`{"data":[
			{"volid":"pbs:backup/ct/9000/2026-11-26T05:01:07Z","vmid":9000,"subtype":"lxc","ctime":1795669267,"size":10},
			{"volid":"pbs:backup/ct/9001/2026-11-26T05:04:16Z","vmid":9001,"subtype":"lxc","ctime":1795669456,"size":20},
			{"volid":"pbs:backup/ct/9000/2026-11-25T05:01:01Z","vmid":"9000","subtype":"lxc","ctime":1795582861,"size":30}]}`))
	}))
	defer server.Close()

	got, err := New(server.URL, "t", false).Backups(context.Background(), "pve", "pbs", 9000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want the two of 9000", got)
	}
	if got[0].VolID != "pbs:backup/ct/9000/2026-11-25T05:01:01Z" || !got[1].Time.Equal(time.Unix(1795669267, 0)) || got[1].Kind != "lxc" {
		t.Errorf("got %+v", got)
	}
}

// An LXC is restored with ostemplate and restore, a VM with archive; both
// with force onto the same VMID, their disks on the given storage.
func TestRestoreMachine(t *testing.T) {
	cases := []struct {
		kind string
		want map[string]string
	}{
		{"lxc", map[string]string{"vmid": "9000", "force": "1", "storage": "local-zfs", "ostemplate": "pbs:backup/x", "restore": "1"}},
		{"qemu", map[string]string{"vmid": "9000", "force": "1", "storage": "local-zfs", "archive": "pbs:backup/x"}},
	}
	for _, c := range cases {
		var form url.Values
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "POST /nodes/pve/" + c.kind:
				body, _ := io.ReadAll(r.Body)
				form, _ = url.ParseQuery(string(body))
				w.Write([]byte(`{"data":"UPID:pve:1"}`))
			case "GET /nodes/pve/tasks/UPID:pve:1/status":
				w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
		}))
		if err := New(server.URL, "t", false).RestoreMachine(context.Background(), "pve", c.kind, 9000, "pbs:backup/x", "local-zfs"); err != nil {
			t.Errorf("%s: %v", c.kind, err)
		}
		server.Close()
		if len(form) != len(c.want) {
			t.Errorf("%s: form = %v, want %v", c.kind, form, c.want)
		}
		for k, v := range c.want {
			if form.Get(k) != v {
				t.Errorf("%s: %s = %q, want %q", c.kind, k, form.Get(k), v)
			}
		}
	}
}

func TestRestoreMachine_AFailedTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Write([]byte(`{"data":"UPID:pve:2"}`))
			return
		}
		w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"unable to restore CT 9000 - permission denied"}}`))
	}))
	defer server.Close()
	err := New(server.URL, "t", false).RestoreMachine(context.Background(), "pve", "lxc", 9000, "pbs:backup/x", "local-zfs")
	if err == nil {
		t.Fatal("want the task's error")
	}
}

func TestStopAndStartMachine(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Write([]byte(`{"data":null}`))
	}))
	defer server.Close()
	c := New(server.URL, "t", false)
	if err := c.StopMachine(context.Background(), "pve", "qemu", 9180); err != nil {
		t.Fatal(err)
	}
	if err := c.StartMachine(context.Background(), "pve", "lxc", 9000); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /nodes/pve/qemu/9180/status/stop", "POST /nodes/pve/lxc/9000/status/start"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("paths = %q, want %q", paths, want)
	}
}
