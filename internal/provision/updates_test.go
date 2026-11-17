package provision

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/imageref"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

// taggedImages also lists tags, as imageref.Resolver does.
type taggedImages struct {
	fakeImages
	tags map[string][]string
}

func (f *taggedImages) Tags(ctx context.Context, image string) ([]string, error) {
	return f.tags[image], nil
}

// urbex updates: the asked tag resolved again, and the newer tags of the
// same shape - per environment applied, changing nothing
// (urbforge/urbex-cli#113).
func TestUpdates(t *testing.T) {
	dir := t.TempDir()
	m := &manifest.Manifest{APIVersion: "urbex/v1", Project: "my-app", Services: []manifest.Service{
		{Name: "api", Runtime: "go", Port: 8080},
		{Name: "cache", Image: "valkey/valkey", Version: "9", Port: 6379},
		{Name: "db", Image: "postgres", Version: "17.6", Port: 5432},
	}}
	o, _, _, _, _ := newOrchestrator(m)
	images := &taggedImages{
		fakeImages: fakeImages{digests: map[string]string{"valkey/valkey:9": "sha256:new9", "postgres:17.6": "sha256:pg176"}},
		tags: map[string][]string{
			"valkey/valkey": {"8", "9", "latest"},
			"postgres":      {"17.6", "17.7", "18.1", "17", "18"},
		},
	}
	o.Images = images
	repo := envrepo.Repo{Path: dir}
	for _, d := range []string{"staging/my-app/cache", "staging/my-app/db", "prod/my-app/db"} {
		if err := os.MkdirAll(filepath.Join(dir, "environments", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []struct{ env, svc, version string }{
		{"staging", "cache", "9@sha256:old9"},
		{"staging", "db", "17.6@sha256:pg176"},
		{"prod", "db", "17.6@sha256:pg176"}, // cache isn't in prod yet
	} {
		if err := repo.SetVersion(v.env, "my-app", v.svc, envrepo.VersionFileState{Version: v.version}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := o.Updates(context.Background(), Options{GitOpsRepoPath: dir})
	if err != nil {
		t.Fatal(err)
	}
	want := []ImageUpdate{
		{Env: "staging", App: "cache", Image: "valkey/valkey", Asked: "9", Running: "9@sha256:old9", NewDigest: true},
		{Env: "staging", App: "db", Image: "postgres", Asked: "17.6", Running: "17.6@sha256:pg176", Updates: imageref.Updates{Minor: "17.7", Major: "18.1"}},
		{Env: "prod", App: "db", Image: "postgres", Asked: "17.6", Running: "17.6@sha256:pg176", Updates: imageref.Updates{Minor: "17.7", Major: "18.1"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if st, _ := repo.Version("staging", "my-app", "cache"); st.Version != "9@sha256:old9" {
		t.Errorf("updates must change nothing, version is %q", st.Version)
	}

	only, err := o.Updates(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir})
	if err != nil || len(only) != 1 || only[0].App != "db" {
		t.Errorf("prod only: %+v, %v", only, err)
	}
}
