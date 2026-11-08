package credentials

import "testing"

func TestHostPath(t *testing.T) {
	t.Setenv(HostWorkspaceEnv, "")
	if got := HostPath("/work/home/.urbex/credentials.yaml"); got != "/work/home/.urbex/credentials.yaml" {
		t.Errorf("outside urbex-op: %q", got)
	}
	t.Setenv(HostWorkspaceEnv, "/Users/me/urbex-work")
	for in, want := range map[string]string{
		"/work/home/.urbex/credentials.yaml": "/Users/me/urbex-work/home/.urbex/credentials.yaml",
		"/work":                              "/Users/me/urbex-work",
		"/workspace/x":                       "/workspace/x",
		"/home/me/.urbex/credentials.yaml":   "/home/me/.urbex/credentials.yaml",
	} {
		if got := HostPath(in); got != want {
			t.Errorf("HostPath(%q) = %q, want %q", in, got, want)
		}
	}
}
