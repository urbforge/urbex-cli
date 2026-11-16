package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/version"
)

func runRoot(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("urbex %s: %v", strings.Join(args, " "), err)
	}
	return out.String()
}

// urbex version and urbex --version print the same line; -o json the fields.
func TestVersionCmd(t *testing.T) {
	want := version.Get().String() + "\n"
	if got := runRoot(t, "version"); got != want {
		t.Errorf("urbex version = %q, want %q", got, want)
	}
	if got := runRoot(t, "--version"); got != want {
		t.Errorf("urbex --version = %q, want %q", got, want)
	}
	var i version.Info
	if err := json.Unmarshal([]byte(runRoot(t, "version", "-o", "json")), &i); err != nil {
		t.Fatal(err)
	}
	if i.Version != version.Get().Version || i.Go == "" || i.Platform == "" {
		t.Errorf("json = %+v", i)
	}
}
