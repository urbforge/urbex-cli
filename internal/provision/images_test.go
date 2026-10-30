package provision

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

// fakeImages resolves every tag to the digest set for it ("sha256:"
// plus the tag, unless digests says otherwise).
type fakeImages struct {
	digests  map[string]string // "image:tag" -> digest
	resolved []string
}

func (f *fakeImages) Resolve(ctx context.Context, image, tag string) (string, error) {
	f.resolved = append(f.resolved, image+":"+tag)
	if d, ok := f.digests[image+":"+tag]; ok {
		return d, nil
	}
	return "sha256:" + tag, nil
}

func stackManifest() *manifest.Manifest {
	return &manifest.Manifest{
		APIVersion: "urbex/v1",
		Project:    "my-app",
		Services: []manifest.Service{
			{Name: "api", Runtime: "go", Port: 8080},
			{Name: "cache", Image: "valkey/valkey", Version: "9", Port: 6379},
		},
	}
}

func imagesOrchestrator(m *manifest.Manifest) (*Orchestrator, *fakeRollout, *fakeImages, *bytes.Buffer, *[]string) {
	o, _, ro, out, synced := newOrchestrator(m)
	images := &fakeImages{digests: map[string]string{}}
	o.Images = images
	return o, ro, images, out, synced
}

func version(t *testing.T, dir, env, service string) envrepo.VersionFileState {
	t.Helper()
	st, err := (envrepo.Repo{Path: dir}).Version(env, "my-app", service)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestApply_PinsTheApplicationsFromImagesOnce(t *testing.T) {
	dir := t.TempDir()
	o, ro, images, out, synced := imagesOrchestrator(stackManifest())
	ro.mainVersion = "a1b2c3d"
	ctx := context.Background()

	if err := o.Apply(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st := version(t, dir, "staging", "cache"); st != (envrepo.VersionFileState{Version: "9@sha256:9"}) {
		t.Errorf("expected the tag pinned to its digest, not following main, got %+v", st)
	}
	if st := version(t, dir, "staging", "api"); st != (envrepo.VersionFileState{Version: "a1b2c3d", Track: true}) {
		t.Errorf("expected the service built from source to follow main, got %+v", st)
	}
	if !strings.Contains(out.String(), "cache: valkey/valkey:9 pinned to sha256:9") {
		t.Errorf("expected the pin in the output:\n%s", out)
	}
	// Pinned before apply's one commit, not in one of its own.
	if len(*synced) != 1 || (*synced)[0] != "urbex apply my-app staging" {
		t.Errorf("unexpected gitops syncs %v", *synced)
	}

	// The tag moves upstream: re-applying keeps the pinned digest.
	images.digests["valkey/valkey:9"] = "sha256:moved"
	images.resolved = nil
	if err := o.Apply(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if st := version(t, dir, "staging", "cache"); st.Version != "9@sha256:9" || len(images.resolved) != 0 {
		t.Errorf("re-apply must keep the pinned digest without resolving, got %+v after %v", st, images.resolved)
	}

	// The manifest asks for another tag: apply says how to move to it.
	o.Manifest.Services[1].Version = "10"
	out.Reset()
	if err := o.Apply(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("third Apply: %v", err)
	}
	if !strings.Contains(out.String(), "urbex.yaml asks for 10 - 'urbex deploy staging --app cache --update' moves it") {
		t.Errorf("expected a note about the changed tag:\n%s", out)
	}
}

func TestApply_NewProdRunsItsImagesWhileWaitingForARelease(t *testing.T) {
	dir := t.TempDir()
	o, ro, _, out, _ := imagesOrchestrator(stackManifest())

	if err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ro.deploys != 1 || strings.Join(ro.deployed, ",") != "cache" {
		t.Errorf("expected only the application from an image deployed, got %d deploys of %v", ro.deploys, ro.deployed)
	}
	if st := version(t, dir, "prod", "api"); st.Version != "" {
		t.Errorf("expected no version for api in a new prod, got %+v", st)
	}
	for _, want := range []string{"cache", "9 (", "no version to run yet", "urbex promote"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

func TestApply_NewProdTakesTheDigestStagingRuns(t *testing.T) {
	dir := t.TempDir()
	o, ro, images, out, _ := imagesOrchestrator(stackManifest())
	ro.mainVersion = "a1b2c3d"
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply staging: %v", err)
	}

	// The tag moves upstream before prod is created.
	images.digests["valkey/valkey:9"] = "sha256:moved"
	images.resolved = nil
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply prod: %v", err)
	}
	if st := version(t, dir, "prod", "cache"); st.Version != "9@sha256:9" || len(images.resolved) != 0 {
		t.Errorf("expected prod on staging's digest without resolving, got %+v after %v", st, images.resolved)
	}
	if !strings.Contains(out.String(), "cache: valkey/valkey:9 pinned to sha256:9, the digest staging runs.") {
		t.Errorf("expected the copied pin in the output:\n%s", out)
	}
}

func TestApply_StackOfImagesWithOnlyProd(t *testing.T) {
	dir := t.TempDir()
	m := &manifest.Manifest{APIVersion: "urbex/v1", Project: "my-app", Environments: []string{"prod"},
		Services: []manifest.Service{{Name: "vault", Image: "vaultwarden/server", Version: "1.34.3", Port: 80}}}
	o, ro, _, out, _ := imagesOrchestrator(m)
	ctx := context.Background()

	if err := o.Apply(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err == nil || !strings.Contains(err.Error(), "has no staging environment") {
		t.Fatalf("expected staging refused for a prod-only stack, got %v", err)
	}
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ro.mainBuilds != 0 || ro.deploys != 1 {
		t.Errorf("expected a deploy and no build, got %d builds, %d deploys", ro.mainBuilds, ro.deploys)
	}
	if !strings.Contains(out.String(), "http://192.168.1.210:80  1.34.3@sha256:1.34.3") {
		t.Errorf("expected the deployed application in the output:\n%s", out)
	}
	if _, err := o.Promote(ctx, dir, false); err == nil || !strings.Contains(err.Error(), "only prod: promote doesn't apply") {
		t.Errorf("expected promote refused for a prod-only stack, got %v", err)
	}
	err := o.Deploy(ctx, Options{Env: "prod", GitOpsRepoPath: dir, Version: "1.4.2"})
	if err == nil || !strings.Contains(err.Error(), "no service built from source") {
		t.Errorf("expected a release deploy refused for a stack of images, got %v", err)
	}
}

func TestDeploy_UpdateResolvesTheTagAgain(t *testing.T) {
	dir := t.TempDir()
	o, _, images, out, synced := imagesOrchestrator(stackManifest())
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	*synced = nil
	if err := o.Deploy(ctx, Options{Env: "prod", GitOpsRepoPath: dir, Update: true}); err != nil {
		t.Fatalf("Deploy --update: %v", err)
	}
	if len(*synced) != 0 || !strings.Contains(out.String(), "cache: already runs valkey/valkey:9 (sha256:9)") {
		t.Errorf("an unchanged digest must push nothing, got %v:\n%s", *synced, out)
	}

	images.digests["valkey/valkey:9"] = "sha256:new"
	out.Reset()
	if err := o.Deploy(ctx, Options{Env: "prod", GitOpsRepoPath: dir, App: "cache", Update: true}); err != nil {
		t.Fatalf("Deploy --app cache --update: %v", err)
	}
	if st := version(t, dir, "prod", "cache"); st.Version != "9@sha256:new" {
		t.Errorf("expected the new digest, got %+v", st)
	}
	if len(*synced) != 1 || (*synced)[0] != "urbex update my-app prod" {
		t.Errorf("unexpected gitops syncs %v", *synced)
	}
	if !strings.Contains(out.String(), "cache: 9@sha256:9 -> 9@sha256:new") {
		t.Errorf("expected the change in the output:\n%s", out)
	}

	*synced = nil
	if err := o.Deploy(ctx, Options{Env: "prod", GitOpsRepoPath: dir, App: "cache", Version: "9.1"}); err != nil {
		t.Fatalf("Deploy --app cache --version: %v", err)
	}
	if st := version(t, dir, "prod", "cache"); st.Version != "9.1@sha256:9.1" {
		t.Errorf("expected the other tag pinned, got %+v", st)
	}
	if len(*synced) != 1 || (*synced)[0] != "urbex deploy 9.1 of cache to my-app prod" {
		t.Errorf("unexpected gitops syncs %v", *synced)
	}
}

func TestDeploy_RefusesWhatDoesNotApplyToImages(t *testing.T) {
	dir := t.TempDir()
	o, _, _, _, _ := imagesOrchestrator(stackManifest())
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, tc := range []struct {
		opts Options
		want string
	}{
		{Options{App: "api", Update: true}, "api is built from source"},
		{Options{App: "nope", Update: true}, `no application "nope"`},
		{Options{App: "cache"}, "--update"},
		{Options{Update: true, Version: "9"}, "--update"},
		{Options{Update: true, FollowMain: true}, "--follow-main is for services built from source"},
	} {
		tc.opts.Env, tc.opts.GitOpsRepoPath = "prod", dir
		if err := o.Deploy(ctx, tc.opts); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Deploy(%+v): expected %q, got %v", tc.opts, tc.want, err)
		}
	}
}

func TestPromote_GivesProdTheDigestsStagingRuns(t *testing.T) {
	dir := t.TempDir()
	o, ro, images, out, _ := imagesOrchestrator(stackManifest())
	ctx := context.Background()
	ro.mainVersion = "a1b2c3d"
	ro.releases = nil
	for _, env := range []string{"staging", "prod"} {
		if err := o.Apply(ctx, Options{Env: env, GitOpsRepoPath: dir}); err != nil {
			t.Fatalf("Apply %s: %v", env, err)
		}
	}
	// Staging updates its cache first.
	images.digests["valkey/valkey:9"] = "sha256:new"
	if err := o.Deploy(ctx, Options{Env: "staging", GitOpsRepoPath: dir, Update: true}); err != nil {
		t.Fatalf("Deploy --update: %v", err)
	}
	if err := o.Deploy(ctx, Options{Env: "staging", GitOpsRepoPath: dir, Version: "1.4.2"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	out.Reset()

	changed, err := o.Promote(ctx, dir, false)
	if err != nil || !changed {
		t.Fatalf("Promote = %v, %v", changed, err)
	}
	if st := version(t, dir, "prod", "cache"); st.Version != "9@sha256:new" {
		t.Errorf("expected prod on staging's digest, got %+v", st)
	}
	if st := version(t, dir, "prod", "api"); st.Version != "1.4.2" {
		t.Errorf("expected prod on staging's release, got %+v", st)
	}
	if !strings.Contains(out.String(), "cache: 9@sha256:9 -> 9@sha256:new") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestCheckMajor_OnlyForApplicationsWithVolumes(t *testing.T) {
	o := &Orchestrator{}
	db := manifest.Service{Name: "db", Image: "postgres", Version: "17", Volumes: []manifest.Volume{{Name: "data", Path: "/data"}}}
	if err := o.checkMajor(db, "17.6", "18", false); err == nil || !strings.Contains(err.Error(), "--allow-major") {
		t.Errorf("expected a new major refused without --allow-major, got %v", err)
	}
	for _, tc := range []struct {
		svc        manifest.Service
		from, to   string
		allowMajor bool
	}{
		{db, "17.6", "17.7", false},
		{db, "17.6", "18", true},
		{db, "", "18", false}, // nothing pinned yet: no data to keep
		{manifest.Service{Name: "cache", Image: "valkey/valkey"}, "8", "9", false},
	} {
		if err := o.checkMajor(tc.svc, tc.from, tc.to, tc.allowMajor); err != nil {
			t.Errorf("checkMajor(%s, %s -> %s, %v): %v", tc.svc.Name, tc.from, tc.to, tc.allowMajor, err)
		}
	}
}

func TestApply_RefusesAUseOfAnUnknownApplication(t *testing.T) {
	m := stackManifest()
	m.Services[0].Uses = []string{"nowhere"}
	o, _, _, _, _ := imagesOrchestrator(m)
	err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "uses nowhere, which urbex.yaml does not declare") {
		t.Errorf("expected uses refused, got %v", err)
	}
}
