package proxmoxapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestParseMountPoint(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  MountPoint
	}{
		{"local-lvm:vm-9101-disk-1,mp=/srv/volumes/db/data,backup=1,size=8G",
			MountPoint{Key: "mp0", Volume: "local-lvm:vm-9101-disk-1", Path: "/srv/volumes/db/data", SizeGB: 8}},
		{"/mnt/pve/nas/media,mp=/srv/shares/media,ro=1",
			MountPoint{Key: "mp0", Volume: "/mnt/pve/nas/media", Path: "/srv/shares/media", ReadOnly: true}},
		{"volume=local:9101/vm-9101-disk-0.raw,mp=/data,size=512M",
			MountPoint{Key: "mp0", Volume: "local:9101/vm-9101-disk-0.raw", Path: "/data", SizeGB: 1}},
		{"local-zfs:subvol-9101-disk-1,mp=/big,size=2T",
			MountPoint{Key: "mp0", Volume: "local-zfs:subvol-9101-disk-1", Path: "/big", SizeGB: 2048}},
	} {
		if got := ParseMountPoint("mp0", tc.value); got != tc.want {
			t.Errorf("ParseMountPoint(%q) = %+v, want %+v", tc.value, got, tc.want)
		}
	}
}

func TestMountPoints_InKeyOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nodes/pve/lxc/9101/config" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`{"data":{"hostname":"media-jellyfin-prod","rootfs":"local-lvm:vm-9101-disk-0,size=5G",
			"mp10":"/mnt/pve/nas/media,mp=/srv/shares/media","mp2":"local-lvm:vm-9101-disk-1,mp=/srv/volumes/app/config,backup=1,size=5G"}}`))
	}))
	defer server.Close()

	mps, err := New(server.URL, "t", false).MountPoints(context.Background(), "pve", 9101)
	if err != nil {
		t.Fatal(err)
	}
	if len(mps) != 2 || mps[0].Key != "mp2" || mps[1].Key != "mp10" || mps[1].Index() != 10 {
		t.Errorf("MountPoints = %+v", mps)
	}
}

func TestAddResizeAndPending(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		switch r.Method + " " + r.URL.Path {
		case "PUT /nodes/pve/lxc/9101/config":
			got = append(got, "config mp1="+form.Get("mp1"))
			w.Write([]byte(`{"data":null}`))
		case "PUT /nodes/pve/lxc/9101/resize":
			got = append(got, "resize "+form.Get("disk")+" "+form.Get("size"))
			w.Write([]byte(`{"data":null}`))
		case "GET /nodes/pve/lxc/9101/pending":
			w.Write([]byte(`{"data":[{"key":"hostname","value":"x"},{"key":"mp1","pending":"local-lvm:10,mp=/srv/volumes/db/data,backup=1"}]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c, ctx := New(server.URL, "t", false), context.Background()
	if err := c.AddVolume(ctx, "pve", 9101, "mp1", "local-lvm", 10, "/srv/volumes/db/data"); err != nil {
		t.Fatal(err)
	}
	if err := c.ResizeVolume(ctx, "pve", 9101, "mp0", 20); err != nil {
		t.Fatal(err)
	}
	pending, err := c.Pending(ctx, "pve", 9101)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"config mp1=local-lvm:10,mp=/srv/volumes/db/data,backup=1", "resize mp0 20G"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(pending, []string{"mp1"}) {
		t.Errorf("Pending = %v, want only the changed key", pending)
	}
}

func TestSizeGB(t *testing.T) {
	for in, want := range map[string]int{"": 0, "2G": 2, "512M": 1, "1T": 1024, "1073741824": 1, "2147483648": 2} {
		if got := sizeGB(in); got != want {
			t.Errorf("sizeGB(%q) = %d, want %d", in, got, want)
		}
	}
}
