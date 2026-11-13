package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/gitea"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/pgadmin"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/rollout"
	"github.com/urbforge/urbex-cli/internal/runner"
)

type fakeRunner struct {
	calls []runner.Command
	err   error
}

func (f *fakeRunner) Run(ctx context.Context, c runner.Command) error {
	f.calls = append(f.calls, c)
	return f.err
}

// runnerFunc runs a function for each command, and succeeds.
type runnerFunc func(runner.Command)

func (f runnerFunc) Run(_ context.Context, c runner.Command) error {
	f(c)
	return nil
}

func validPlatformConfig() *platform.Config {
	return &platform.Config{
		Domain: "example.com",
		Git:    platform.GitConfig{GiteaURL: "https://git.example.com"},
		Proxmox: platform.ProxmoxConfig{
			APIURL:       "https://proxmox.example.internal:8006/api2/json",
			Node:         "pve",
			StoragePool:  "local-lvm",
			LXCTemplate:  "local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst",
			SSHPublicKey: "ssh-ed25519 AAAA... test",
			VMIDBase:     9000,
			Network: platform.NetworkConfig{
				CIDR:           "192.168.1.0/24",
				Gateway:        "192.168.1.1",
				BaseHostOffset: 200,
			},
		},
	}
}

func validManifest() *manifest.Manifest {
	return &manifest.Manifest{
		APIVersion: "urbex/v1",
		Project:    "my-app",
		Services: []manifest.Service{
			{Name: "api", Runtime: "go", Port: 8080},
		},
	}
}

func validCredentials() *credentials.Credentials {
	return &credentials.Credentials{
		ProxmoxToken:        "pve-token",
		AgeKey:              "AGE-SECRET-KEY-13E9XGFYJN5M6PKTQJFHSXX3MCEZS9XT2EA88VFZ3Z3RFA3VHXGGQ6LS0RC",
		GiteaToken:          "gitea-token",
		KomodoAPIKey:        "K_key_K",
		KomodoAPISecret:     "S_secret_S",
		KomodoOnboardingKey: "O_onboard_O",
		KomodoWebhookSecret: "hook-secret",
	}
}

// fakeRollout stands in for internal/rollout: it writes the environment
// folders like the real one, and "runs" whatever the GitOps repo asks.
type fakeRollout struct {
	telemetry                    []string
	projects, envSetups, deploys int
	deployed                     []string // the services of the last Deploy
	reconciles                   int
	missing                      []string // images MissingImages reports
	waitErr                      error
	removedEnvs                  []rollout.Target
	serversRemoved               int
	projectsRemoved              []string
	mainBuilds                   int
	// mainVersion is what a build of main sets following environments to.
	mainVersion string
	releases    []gitea.Tag
	repo        *envrepo.Repo
	services    []manifest.Service
	// pg are the postgres providers' fake servers, by service: one is
	// made the first time it is asked for.
	pg map[string]*fakePG
	// deployOrder are the services of every Deploy, in order.
	deployOrder []string
}

func (f *fakeRollout) SetupProject(ctx context.Context, project string, services []manifest.Service) error {
	f.projects++
	return nil
}

func (f *fakeRollout) WriteEnv(t rollout.Target, repo envrepo.Repo) error {
	for _, svc := range t.Services {
		image, follow := "192.168.1.200:3000/urbex/"+t.Project+"-"+svc.Name, rollout.FollowsMainByDefault(t.Env)
		if svc.FromImage() {
			image, follow = svc.Image, false
		}
		if err := repo.WriteService(t.Env, t.Project, svc, image, follow, t.Links[svc.Name]); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeRollout) SetupEnv(ctx context.Context, t rollout.Target, repo envrepo.Repo) error {
	f.envSetups++
	f.repo, f.services = &repo, t.Services
	return f.WriteEnv(t, repo)
}

func (f *fakeRollout) MissingImages(ctx context.Context, project string, services []manifest.Service, version string) ([]string, error) {
	return f.missing, nil
}

func (f *fakeRollout) DeployTelemetry(ctx context.Context, t rollout.Target) error {
	f.telemetry = append(f.telemetry, t.TelemetryHosts()...)
	return nil
}
func (f *fakeRollout) Deploy(ctx context.Context, t rollout.Target) error {
	f.deploys++
	f.deployed = nil
	for _, svc := range t.Services {
		f.deployed = append(f.deployed, svc.Name)
		f.deployOrder = append(f.deployOrder, svc.Name)
	}
	return nil
}

// BuildMain stands in for the release Action: it sets every following
// environment of the last target seen to mainVersion.
func (f *fakeRollout) BuildMain(ctx context.Context, project string) error {
	f.mainBuilds++
	if f.repo == nil {
		return nil
	}
	for _, env := range []string{"staging", "prod"} {
		for _, svc := range f.services {
			st, _ := f.repo.Version(env, project, svc.Name)
			if st.Track {
				f.repo.SetVersion(env, project, svc.Name, envrepo.VersionFileState{Version: f.mainVersion, Track: true})
			}
		}
	}
	return nil
}

func (f *fakeRollout) Releases(ctx context.Context, project string) ([]gitea.Tag, error) {
	return f.releases, nil
}

func (f *fakeRollout) Reconcile(ctx context.Context) error {
	f.reconciles++
	return nil
}

func (f *fakeRollout) WaitDeployed(ctx context.Context, t rollout.Target, repo envrepo.Repo) ([]rollout.Deployed, error) {
	if f.waitErr != nil {
		return nil, f.waitErr
	}
	var deployed []rollout.Deployed
	for _, svc := range t.Services {
		st, err := repo.Version(t.Env, t.Project, svc.Name)
		if err != nil {
			return nil, err
		}
		want := st.Version
		deployed = append(deployed, rollout.Deployed{
			Service: svc.Name,
			Want:    want,
			Track:   st.Track,
			State:   "running",
			Image:   "192.168.1.200:3000/urbex/" + t.Project + "-" + svc.Name + ":" + want,
		})
	}
	return deployed, nil
}

func (f *fakeRollout) RemoveEnv(ctx context.Context, t rollout.Target) error {
	f.removedEnvs = append(f.removedEnvs, t)
	return nil
}

func (f *fakeRollout) RemoveServers(ctx context.Context, t rollout.Target) error {
	f.serversRemoved++
	return nil
}

func (f *fakeRollout) Shell(project, env, service string) pgadmin.Shell {
	if f.pg == nil {
		f.pg = map[string]*fakePG{}
	}
	if f.pg[service] == nil {
		f.pg[service] = newFakePG()
	}
	return f.pg[service]
}

func (f *fakeRollout) RemoveProject(ctx context.Context, project string, services []manifest.Service) error {
	f.projectsRemoved = append(f.projectsRemoved, project)
	return nil
}

// newOrchestrator returns an Orchestrator on fakes, recording what it
// syncs to the GitOps repo.
func newOrchestrator(m *manifest.Manifest) (o *Orchestrator, r *fakeRunner, ro *fakeRollout, out *bytes.Buffer, synced *[]string) {
	r, ro, out, synced = &fakeRunner{}, &fakeRollout{}, &bytes.Buffer{}, &[]string{}
	o = &Orchestrator{
		Manifest:    m,
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         out,
		Rollout:     ro,
		SyncGitOps: func(ctx context.Context, path, message string) error {
			*synced = append(*synced, message)
			return nil
		},
	}
	return o, r, ro, out, synced
}

func TestPlan_RunsTerraformInitAndPlanOnly(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	if err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if len(r.calls) != 2 {
		t.Fatalf("expected 2 commands (init, plan), got %d: %+v", len(r.calls), r.calls)
	}
	if r.calls[0].Args[0] != "init" || r.calls[1].Args[0] != "plan" {
		t.Errorf("unexpected command sequence: %+v", r.calls)
	}

	for _, want := range []string{
		"terraform/projects/my-app/staging/main.tf",
		"terraform/projects/my-app/staging/terraform.auto.tfvars.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected %s to be materialized: %v", want, err)
		}
	}
}

func TestApply_RunsTerraformThenAnsible(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(r.calls) != 3 {
		t.Fatalf("expected 3 commands (init, apply, ansible-playbook), got %d: %+v", len(r.calls), r.calls)
	}
	if r.calls[0].Args[0] != "init" || r.calls[1].Args[0] != "apply" || r.calls[2].Name != "ansible-playbook" {
		t.Errorf("unexpected command sequence: %+v", r.calls)
	}
}

func TestPrepare_AllocatesAndPersistsIPsAndVMIDs(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	if err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	tfvarsPath := filepath.Join(dir, "terraform", "projects", "my-app", "staging", "terraform.auto.tfvars.json")
	data, err := os.ReadFile(tfvarsPath)
	if err != nil {
		t.Fatalf("reading tfvars: %v", err)
	}
	var parsed tfVars
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshaling tfvars: %v", err)
	}

	svc, ok := parsed.Services["my-app-api-staging"]
	if !ok {
		t.Fatalf("expected hostname my-app-api-staging in services, got: %+v", parsed.Services)
	}
	// BaseHostOffset 200 -> project allocations start at .210 (see prepare()).
	if svc.IPAddress != "192.168.1.210" {
		t.Errorf("IPAddress = %q, want 192.168.1.210", svc.IPAddress)
	}
	if svc.VMID != 9100 {
		t.Errorf("VMID = %d, want 9100 (VMIDBase 9000 + 100)", svc.VMID)
	}
	if svc.Cores != manifest.DefaultCPU {
		t.Errorf("Cores = %d, want default %d", svc.Cores, manifest.DefaultCPU)
	}

	// Re-running Plan must reuse the same allocation, not hand out a new one.
	r2 := &fakeRunner{}
	o.Runner = r2
	if err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("second Plan: %v", err)
	}
	data2, err := os.ReadFile(tfvarsPath)
	if err != nil {
		t.Fatalf("reading tfvars after second plan: %v", err)
	}
	var parsed2 tfVars
	if err := json.Unmarshal(data2, &parsed2); err != nil {
		t.Fatalf("unmarshaling tfvars after second plan: %v", err)
	}
	if parsed2.Services["my-app-api-staging"] != svc {
		t.Errorf("expected the same allocation on re-plan, got %+v then %+v", svc, parsed2.Services["my-app-api-staging"])
	}
}

func TestPlan_InvalidEnv(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	err := o.Plan(context.Background(), Options{Env: "development", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for an invalid environment")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for an invalid environment, got: %+v", r.calls)
	}
}

func TestPlan_NoServices(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	m := validManifest()
	m.Services = nil

	o := &Orchestrator{
		Manifest:    m,
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for a manifest with no services")
	}
}

func TestDeploy_RequiresPriorApply(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error when the service has no allocation yet (apply not run)")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run when unprovisioned, got: %+v", r.calls)
	}
}

func TestDeploy_SetsTheVersionInTheGitOpsRepoAndWaits(t *testing.T) {
	dir := t.TempDir()
	o, r, ro, out, synced := newOrchestrator(validManifest())
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r.calls, ro.envSetups, *synced = nil, 0, nil

	if err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir, Version: "1.4.2"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if len(r.calls) != 0 {
		t.Fatalf("expected deploy to run no terraform/ansible at all, got %+v", r.calls)
	}
	version, err := os.ReadFile(filepath.Join(dir, "environments", "staging", "my-app", "api", "version.env"))
	if err != nil || !strings.Contains(string(version), "VERSION=1.4.2\n") {
		t.Errorf("expected the version written to the gitops repo (%v):\n%s", err, version)
	}
	if len(*synced) != 1 || (*synced)[0] != "urbex deploy 1.4.2 to my-app staging" {
		t.Errorf("expected exactly one push of the gitops repo, got %v", *synced)
	}
	// Deploying is a commit: touching the Stacks or telling Komodo to
	// deploy would bypass - and race with - the GitOps path.
	if ro.envSetups != 0 || ro.deploys != 0 {
		t.Errorf("deploy must not re-declare stacks or deploy explicitly, got setups=%d deploys=%d", ro.envSetups, ro.deploys)
	}
	if !strings.Contains(out.String(), "http://192.168.1.210:8080  1.4.2 (192.168.1.200:3000/urbex/my-app-api:1.4.2)") {
		t.Errorf("expected the deployed service in the output:\n%s", out)
	}
	// Staging followed main; deploying a version pins it, and says so.
	if st, _ := (envrepo.Repo{Path: dir}).Version("staging", "my-app", "api"); st.Track {
		t.Errorf("deploying a version must stop following main, got %+v", st)
	}
	if !strings.Contains(out.String(), "no longer follows main") {
		t.Errorf("expected a note that staging stopped following main:\n%s", out)
	}
}

func TestDeploy_RefusesAVersionThatWasNotBuilt(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, _, synced := newOrchestrator(validManifest())
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	*synced = nil
	ro.missing = []string{"api"}

	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir, Version: "9.9.9"})
	if err == nil || !strings.Contains(err.Error(), "9.9.9 has no image for [api]") {
		t.Fatalf("expected a missing image error, got %v", err)
	}
	if len(*synced) != 0 {
		t.Errorf("nothing must be pushed for a version that doesn't exist, got %v", *synced)
	}
	for _, bad := range []string{"", "v1.4.2", "1.4", "latest", "abc12", "ABC1234", "g123456"} {
		if err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir, Version: bad}); err == nil || !strings.Contains(err.Error(), "invalid version") {
			t.Errorf("Deploy(%q): expected an invalid version error, got %v", bad, err)
		}
	}
}

func TestPromote_PutsTheReleaseStagingRunsInProd(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, out, synced := newOrchestrator(validManifest())
	ctx := context.Background()

	if _, err := o.Promote(ctx, dir, false); err == nil || !strings.Contains(err.Error(), "urbex apply staging") {
		t.Fatalf("expected promote before any apply to say so, got %v", err)
	}
	ro.mainVersion = "a1b2c3d"
	for _, env := range []string{"staging", "prod"} {
		if err := o.Apply(ctx, Options{Env: env, GitOpsRepoPath: dir}); err != nil {
			t.Fatalf("Apply %s: %v", env, err)
		}
	}

	// Staging runs a commit of main that was never released.
	_, err := o.Promote(ctx, dir, false)
	if err == nil || !strings.Contains(err.Error(), "commit a1b2c3d, which is not a release") {
		t.Fatalf("expected promote of an unreleased commit to point at urbex release, got %v", err)
	}

	// Once released, promote finds the release by its commit.
	ro.releases = []gitea.Tag{
		{Name: "v1.4.1", Commit: "0000000000000000000000000000000000000000"},
		{Name: "v1.4.2", Commit: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"},
	}
	*synced = nil
	changed, err := o.Promote(ctx, dir, false)
	if err != nil || !changed {
		t.Fatalf("Promote = %v, %v; want a change", changed, err)
	}
	prod, _ := (envrepo.Repo{Path: dir}).Version("prod", "my-app", "api")
	if prod != (envrepo.VersionFileState{Version: "1.4.2"}) {
		t.Errorf("expected prod pinned to release 1.4.2, got %+v", prod)
	}
	if staging, _ := (envrepo.Repo{Path: dir}).Version("staging", "my-app", "api"); !staging.Track {
		t.Error("promoting must leave staging following main")
	}
	if len(*synced) != 1 || (*synced)[0] != "urbex promote staging to my-app prod" {
		t.Errorf("unexpected gitops syncs %v", *synced)
	}
	if !strings.Contains(out.String(), "api: none -> 1.4.2") {
		t.Errorf("expected the version change in the output:\n%s", out)
	}

	*synced = nil
	changed, err = o.Promote(ctx, dir, false)
	if err != nil || changed || len(*synced) != 0 {
		t.Errorf("a second promote must change and push nothing, got changed=%v err=%v synced=%v", changed, err, *synced)
	}
}

func TestDeploy_FollowMainResumesTracking(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, out, _ := newOrchestrator(validManifest())
	ctx := context.Background()
	ro.mainVersion = "a1b2c3d"
	if err := o.Apply(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Deploy(ctx, Options{Env: "staging", GitOpsRepoPath: dir, Version: "1.4.2"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	ro.mainVersion, ro.mainBuilds = "f00ba47", 0
	out.Reset()

	if err := o.Deploy(ctx, Options{Env: "staging", GitOpsRepoPath: dir, FollowMain: true}); err != nil {
		t.Fatalf("Deploy --follow-main: %v", err)
	}
	st, _ := (envrepo.Repo{Path: dir}).Version("staging", "my-app", "api")
	if st != (envrepo.VersionFileState{Version: "f00ba47", Track: true}) || ro.mainBuilds != 1 {
		t.Errorf("expected staging to follow main again and run main's head, got %+v after %d builds", st, ro.mainBuilds)
	}
	if !strings.Contains(out.String(), "staging follows main") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestValidVersion(t *testing.T) {
	for _, ok := range []string{"1.4.2", "0.1.0", "a1b2c3d", "a1b2c3d4e5f6"} {
		if !ValidVersion(ok) {
			t.Errorf("ValidVersion(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "v1.4.2", "1.4", "a1b2c3", "a1b2c3d4e5f60", "main"} {
		if ValidVersion(bad) {
			t.Errorf("ValidVersion(%q) = true", bad)
		}
	}
}

func TestDeploy_PartiallyProvisionedFailsListingAllMissing(t *testing.T) {
	dir := t.TempDir()

	m := validManifest()
	m.Services = append(m.Services, manifest.Service{Name: "worker", Runtime: "go", Port: 9000})

	o := &Orchestrator{
		Manifest:    m,
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	// Provision only the first service via Plan+manual ledger write is
	// overkill here; instead apply the full manifest, then deploy a
	// manifest that adds a third, never-applied service.
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	o.Manifest.Services = append(o.Manifest.Services, manifest.Service{Name: "never-applied", Runtime: "go", Port: 9001})
	deployRunner := &fakeRunner{}
	o.Runner = deployRunner

	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error listing the unprovisioned service")
	}
	if len(deployRunner.calls) != 0 {
		t.Errorf("expected no commands run when any service is unprovisioned, got: %+v", deployRunner.calls)
	}
}

func TestDestroy_NothingAppliedIsANoOp(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run when nothing was applied, got: %+v", r.calls)
	}
}

func TestDestroy_RunsTerraformDestroyAndClearsLedger(t *testing.T) {
	dir := t.TempDir()

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ledgerPath := filepath.Join(dir, "state", "allocations.json")
	before, err := allocation.Load(ledgerPath, 9100, 210)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := before.Lookup("my-app-api-staging"); !ok {
		t.Fatal("expected an allocation to exist after Apply")
	}

	destroyRunner := &fakeRunner{}
	o.Runner = destroyRunner

	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if len(destroyRunner.calls) != 2 {
		t.Fatalf("expected 2 commands (terraform init, terraform destroy), got %d: %+v", len(destroyRunner.calls), destroyRunner.calls)
	}
	if destroyRunner.calls[0].Args[0] != "init" || destroyRunner.calls[1].Args[0] != "destroy" {
		t.Errorf("unexpected command sequence: %+v", destroyRunner.calls)
	}

	after, err := allocation.Load(ledgerPath, 9100, 210)
	if err != nil {
		t.Fatalf("Load (after destroy): %v", err)
	}
	if _, ok := after.Lookup("my-app-api-staging"); ok {
		t.Error("expected the allocation to be removed after Destroy")
	}
}

func TestDestroy_MissingCredentials(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: &credentials.Credentials{},
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for missing credentials")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for missing credentials, got: %+v", r.calls)
	}
}

func TestPlan_MissingCredentials(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}

	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: &credentials.Credentials{},
		Runner:      r,
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}

	err := o.Plan(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir})
	if err == nil {
		t.Fatal("expected an error for missing credentials")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for missing credentials, got: %+v", r.calls)
	}
}

func TestApply_SetsUpProjectAndAnEmptyProdEnvironment(t *testing.T) {
	dir := t.TempDir()
	o, r, ro, out, synced := newOrchestrator(validManifest())

	if err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if ro.projects != 1 || ro.envSetups != 1 {
		t.Errorf("expected the project and the environment to be set up once, got %d/%d", ro.projects, ro.envSetups)
	}
	inventory, err := os.ReadFile(filepath.Join(dir, "ansible", "projects", "my-app", "prod", "inventory.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"gitea_registry: git.urbex.example.com:3000",
		"komodo_core_address: http://komodo.urbex.example.com:9120",
		"komodo_version: 2.3.4",
		"ansible_host: 192.168.1.210",
	} {
		if !strings.Contains(string(inventory), want) {
			t.Errorf("expected %q in inventory:\n%s", want, inventory)
		}
	}
	// The LXCs reach the platform by name: the only addresses are the
	// hosts Ansible connects to (ADR-0027).
	ip := regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)
	for _, line := range strings.Split(string(inventory), "\n") {
		if !strings.Contains(line, "ansible_host:") && ip.MatchString(line) {
			t.Errorf("inventory holds an address: %q", line)
		}
	}
	if strings.Contains(string(inventory), "O_onboard_O") || strings.Contains(string(inventory), "AGE-SECRET-KEY") {
		t.Errorf("the inventory must hold no secrets:\n%s", inventory)
	}
	ansible := r.calls[len(r.calls)-1]
	env := strings.Join(ansible.Env, " ")
	if ansible.Name != "ansible-playbook" || len(ansible.Env) != 2 ||
		!strings.Contains(env, "URBEX_KOMODO_ONBOARDING_KEY=O_onboard_O") || !strings.Contains(env, "URBEX_AGE_KEY=AGE-SECRET-KEY-") {
		t.Errorf("expected ansible last, with only the onboarding and age keys in its env, got %s %d vars", ansible.Name, len(ansible.Env))
	}
	for _, f := range []string{"compose.yaml", "config.env", "secrets.sops.env"} {
		if _, err := os.Stat(filepath.Join(dir, "environments", "prod", "my-app", "api", f)); err != nil {
			t.Errorf("expected %s in the environment's folder: %v", f, err)
		}
	}
	if len(*synced) != 1 || (*synced)[0] != "urbex apply my-app prod" {
		t.Errorf("unexpected gitops syncs %v", *synced)
	}
	// A new environment has no version: nothing to deploy, and apply says
	// how to get there.
	if ro.deploys != 0 {
		t.Errorf("an environment without a version must not be deployed, got %d deploys", ro.deploys)
	}
	for _, want := range []string{"no version to run yet", "http://192.168.1.210:8080", "urbex release", "urbex promote"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

func TestApply_StagingFollowsMainAndRunsItsHead(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, out, _ := newOrchestrator(validManifest())
	ro.mainVersion = "a1b2c3d"

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ro.mainBuilds != 1 {
		t.Errorf("expected apply to build main once for a new staging, got %d", ro.mainBuilds)
	}
	for _, want := range []string{"http://192.168.1.210:8080  a1b2c3d", "staging follows main"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

// The push that creates main on Gitea can still reach the webhook created
// right after it: Komodo then builds main and sets staging to it while
// apply runs. Staging is still new, and said to follow main.
func TestApply_StagingFollowsMainEvenIfKomodoSetsItFirst(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, out, _ := newOrchestrator(validManifest())
	ro.mainVersion = "a1b2c3d"
	o.SyncGitOps = func(ctx context.Context, path, message string) error {
		return ro.BuildMain(ctx, o.Manifest.Project)
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(out.String(), "staging follows main") {
		t.Errorf("expected staging to be said to follow main:\n%s", out)
	}
}

func TestApply_RedeploysAnEnvironmentThatHasAVersion(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, out, _ := newOrchestrator(validManifest())
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := o.Deploy(ctx, Options{Env: "staging", GitOpsRepoPath: dir, Version: "1.4.2"}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	out.Reset()

	// E.g. onto a recreated LXC: nothing changes in the repo, so Komodo
	// has to be told.
	if err := o.Apply(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if ro.deploys != 1 {
		t.Errorf("expected re-apply to deploy explicitly once, got %d", ro.deploys)
	}
	if !strings.Contains(out.String(), "1.4.2 (192.168.1.200:3000/urbex/my-app-api:1.4.2)") {
		t.Errorf("expected the running version in the output:\n%s", out)
	}
}

func TestApply_PublicServiceReportsQuickTunnelURL(t *testing.T) {
	dir := t.TempDir()
	m := validManifest()
	m.Services[0].Public = true
	o, _, _, out, _ := newOrchestrator(m)
	o.Config.Cloudflare.QuickTunnel = true
	o.QuickTunnel = func(ctx context.Context, ip string) (string, error) {
		if ip != "192.168.1.210" {
			t.Errorf("expected to look up the service's own IP, got %q", ip)
		}
		return "https://random-words.trycloudflare.com", nil
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(out.String(), "https://random-words.trycloudflare.com") {
		t.Errorf("expected the quick tunnel URL in the output:\n%s", out)
	}
}

func TestApply_QuickTunnelNotReadyIsReportedNotFatal(t *testing.T) {
	dir := t.TempDir()
	m := validManifest()
	m.Services[0].Public = true
	o, _, _, out, _ := newOrchestrator(m)
	o.Config.Cloudflare.QuickTunnel = true
	o.QuickTunnel = func(ctx context.Context, ip string) (string, error) {
		return "", errors.New("connection refused")
	}

	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("a quick tunnel not answering yet must not fail Apply: %v", err)
	}
	if !strings.Contains(out.String(), "quick tunnel not ready") {
		t.Errorf("expected a quick tunnel warning in the output:\n%s", out)
	}
}

func TestDeploy_FailureToConvergeIsReported(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, _, synced := newOrchestrator(validManifest())
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	*synced = nil
	ro.waitErr = errors.New("my-app (staging) did not converge: api (want 1.4.2, unhealthy)")

	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir, Version: "1.4.2"})
	if err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("expected the convergence failure, got %v", err)
	}
	if len(*synced) != 1 {
		t.Errorf("the version is pushed regardless - the repo is what should run - got %d syncs", len(*synced))
	}
}

func TestDeploy_RequiresBootstrappedPlatformCredentials(t *testing.T) {
	dir := t.TempDir()
	o, _, _, _, _ := newOrchestrator(validManifest())
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	o.Credentials.KomodoAPIKey = ""
	err := o.Deploy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir, Version: "1.4.2"})
	if err == nil || !strings.Contains(err.Error(), "URBEX_KOMODO_API_KEY") {
		t.Errorf("expected a missing platform credential error, got %v", err)
	}
}

func TestDestroy_RemovesStacksFolderServersAndFinallyTheProject(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, _, _ := newOrchestrator(validManifest())
	ctx := context.Background()
	for _, env := range []string{"staging", "prod"} {
		if err := o.Apply(ctx, Options{Env: env, GitOpsRepoPath: dir}); err != nil {
			t.Fatalf("Apply %s: %v", env, err)
		}
	}

	if err := o.Destroy(ctx, Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(ro.removedEnvs) != 1 || ro.removedEnvs[0].Env != "staging" || ro.serversRemoved != 1 {
		t.Errorf("expected staging's stacks and servers to be removed, got %+v / %d", ro.removedEnvs, ro.serversRemoved)
	}
	if _, err := os.Stat(filepath.Join(dir, "environments", "staging", "my-app")); !os.IsNotExist(err) {
		t.Errorf("expected staging's folder to be deleted from the gitops repo, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "environments", "prod", "my-app", "api", "compose.yaml")); err != nil {
		t.Errorf("prod's folder must be untouched: %v", err)
	}
	if len(ro.projectsRemoved) != 0 {
		t.Errorf("the project's builds must stay while prod exists, got %v", ro.projectsRemoved)
	}

	if err := o.Destroy(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy prod: %v", err)
	}
	if len(ro.projectsRemoved) != 1 || ro.projectsRemoved[0] != "my-app" {
		t.Errorf("expected the project's builds to go with its last environment, got %v", ro.projectsRemoved)
	}
}

func TestPublish_PushesAndReconciles(t *testing.T) {
	dir := t.TempDir()
	o, _, ro, _, synced := newOrchestrator(validManifest())
	opts := Options{Env: "staging", GitOpsRepoPath: dir}

	if err := o.Publish(context.Background(), opts, "set secret X of api in"); err == nil {
		t.Fatal("expected publishing to an environment that was never applied to fail")
	}
	if err := o.Apply(context.Background(), opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	*synced = nil
	if err := o.Publish(context.Background(), opts, "set secret X of api in"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(*synced) != 1 || (*synced)[0] != "urbex set secret X of api in my-app staging" || ro.reconciles != 1 {
		t.Errorf("expected one push and one reconcile, got %v / %d", *synced, ro.reconciles)
	}
}

// Destroy works from what Gitea has, removes the environment's folder
// only after terraform destroy - right before committing, so a release
// Action updating it meanwhile can't conflict - and leaves nothing of the
// environment behind in the GitOps repo.
func TestDestroy_RefreshesAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	o := &Orchestrator{
		Manifest:    validManifest(),
		Config:      validPlatformConfig(),
		Credentials: validCredentials(),
		Runner:      &fakeRunner{},
		Out:         &bytes.Buffer{},
		Rollout:     &fakeRollout{},
	}
	if err := o.Apply(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	folder := filepath.Join(dir, "environments", "staging", "my-app")
	refreshes := 0
	o.RefreshGitOps = func(context.Context, string) error { refreshes++; return nil }
	folderAtTerraformDestroy := false
	o.Runner = runnerFunc(func(cmd runner.Command) {
		if len(cmd.Args) > 0 && cmd.Args[0] == "destroy" {
			_, err := os.Stat(folder)
			folderAtTerraformDestroy = err == nil
		}
	})
	if err := o.Destroy(context.Background(), Options{Env: "staging", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if refreshes < 2 {
		t.Errorf("expected the gitops repo to be refreshed before reading it and before removing the folder, got %d refreshes", refreshes)
	}
	if !folderAtTerraformDestroy {
		t.Error("the environment's folder should still exist while terraform destroys the LXCs")
	}
	for _, gone := range []string{
		folder,
		filepath.Join(dir, "terraform", "projects", "my-app"),
		filepath.Join(dir, "ansible", "projects", "my-app"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should be gone after destroying the project's only environment", gone)
		}
	}
}
