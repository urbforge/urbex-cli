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
		if r.URL.Path != "/nodes/pve/lxc" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "PVEAPIToken=user@pve!urbex=secret" {
			t.Errorf("unexpected Authorization header: %s", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"name":"urbex-gitea"},{"name":"urbex-komodo"}]}`))
	}))
	defer server.Close()

	client := New(server.URL, "user@pve!urbex=secret")

	hostnames, err := client.ListContainerHostnames(context.Background(), "pve")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sort.Strings(hostnames)
	want := []string{"urbex-gitea", "urbex-komodo"}
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

	client := New(server.URL, "bad-token")

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

	client := New(server.URL, "user@pve!urbex=secret")

	hostnames, err := client.ListContainerHostnames(context.Background(), "pve")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hostnames) != 0 {
		t.Errorf("expected no hostnames, got %v", hostnames)
	}
}
