package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/urbforge/urbex-cli/internal/progress"
)

// status --progress shows the latest run recorded on this machine, with
// no platform, credentials or GitOps repo needed.
func TestStatusProgress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	cmd := newStatusCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--progress"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --progress: %v", err)
	}
	if !strings.Contains(out.String(), "No bootstrap or apply recorded yet") {
		t.Errorf("with nothing recorded:\n%s", out)
	}

	dir, err := progressDir()
	if err != nil {
		t.Fatal(err)
	}
	tr, err := progress.Start(dir, "apply", "urbex apply staging (hello)", []progress.PhaseSpec{
		{ID: "lxcs", Title: "Create the LXCs", Estimate: time.Minute},
		{ID: "build", Title: "Build main", Estimate: 4 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.Begin("lxcs")
	out.Reset()
	cmd = newStatusCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--progress"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --progress: %v", err)
	}
	tr.Finish(nil)
	for _, want := range []string{"urbex apply staging (hello): running for", "▶ Create the LXCs", "· Build main       about 4m00s"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status --progress lacks %q:\n%s", want, out)
		}
	}
}
