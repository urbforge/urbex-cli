package proxmoxapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
)

func TestListContainerHostnames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "PVEAPIToken=user@pve!urbex=secret" {
			t.Errorf("unexpected Authorization header: %s", got)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/nodes/pve/lxc":
			w.Write([]byte(`{"data":[{"name":"urbex-gitea"},{"name":"urbex-komodo"}]}`))
		case "/nodes/pve/qemu": // the VMs are machines too (ADR-0033)
			w.Write([]byte(`{"data":[{"name":"hello-backup-prod"}]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := New(server.URL, "user@pve!urbex=secret", false)

	hostnames, err := client.ListContainerHostnames(context.Background(), "pve")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sort.Strings(hostnames)
	want := []string{"hello-backup-prod", "urbex-gitea", "urbex-komodo"}
	if !reflect.DeepEqual(hostnames, want) {
		t.Errorf("hostnames = %v, want %v", hostnames, want)
	}
}

func TestListContainerHostnames_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid token"))
	}))
	defer server.Close()

	client := New(server.URL, "bad-token", false)

	_, err := client.ListContainerHostnames(context.Background(), "pve")
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestListContainerHostnames_EmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	client := New(server.URL, "user@pve!urbex=secret", false)

	hostnames, err := client.ListContainerHostnames(context.Background(), "pve")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hostnames) != 0 {
		t.Errorf("expected no hostnames, got %v", hostnames)
	}
}

// Proxmox returns a container's vmid as a number, but some versions and
// endpoints use a string: both are accepted.
func TestListContainerIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nodes/pve/qemu" {
			w.Write([]byte(`{"data":[{"name":"hello-backup-prod","vmid":9102}]}`))
			return
		}
		w.Write([]byte(`{"data":[{"name":"urbex-gitea","vmid":9000},{"name":"hello-api-prod","vmid":"9101"}]}`))
	}))
	defer server.Close()

	ids, err := New(server.URL, "t", false).ListContainerIDs(context.Background(), "pve")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{9000, 9101, 9102}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}
