package proxmoxapi

import "testing"

func TestParseDevice(t *testing.T) {
	for value, want := range map[string]string{
		"/dev/net/tun":                      "/dev/net/tun",
		"/dev/dri/renderD128,gid=104":       "/dev/dri/renderD128",
		"path=/dev/dri/card0,mode=0660":     "/dev/dri/card0",
		"mode=0666,path=/dev/net/tun,uid=0": "/dev/net/tun",
	} {
		if d := ParseDevice("dev2", value); d.Path != want || d.Index() != 2 {
			t.Errorf("%q: got %+v, want path %s, index 2", value, d, want)
		}
	}
}
