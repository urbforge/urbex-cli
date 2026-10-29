package provision

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
	"github.com/urbforge/urbex-cli/internal/runner"
)

// fakeVolumes stands in for Proxmox's LXC configuration: the mount
// points of each VMID, with new ones pending until a restart.
type fakeVolumes struct {
	mps      map[int][]proxmoxapi.MountPoint
	pending  map[int][]string
	calls    []string
	reboots  []int
	hotplugs bool // new mount points apply at once, without a restart
	stuck    bool // a restart leaves the pending mount points pending
}

func newFakeVolumes() *fakeVolumes {
	return &fakeVolumes{mps: map[int][]proxmoxapi.MountPoint{}, pending: map[int][]string{}}
}

func (f *fakeVolumes) MountPoints(_ context.Context, _ string, vmid int) ([]proxmoxapi.MountPoint, error) {
	return slices.Clone(f.mps[vmid]), nil
}

func (f *fakeVolumes) AddVolume(_ context.Context, _ string, vmid int, key, storage string, sizeGB int, path string) error {
	f.calls = append(f.calls, fmt.Sprintf("add %d %s %s:%d %s", vmid, key, storage, sizeGB, path))
	f.mps[vmid] = append(f.mps[vmid], proxmoxapi.MountPoint{Key: key, Volume: fmt.Sprintf("%s:vm-%d-disk-%s", storage, vmid, key), Path: path, SizeGB: sizeGB})
	if !f.hotplugs {
		f.pending[vmid] = append(f.pending[vmid], key)
	}
	return nil
}

func (f *fakeVolumes) ResizeVolume(_ context.Context, _ string, vmid int, key string, sizeGB int) error {
	f.calls = append(f.calls, fmt.Sprintf("resize %d %s %d", vmid, key, sizeGB))
	for i, mp := range f.mps[vmid] {
		if mp.Key == key {
			f.mps[vmid][i].SizeGB = sizeGB
		}
	}
	return nil
}

func (f *fakeVolumes) Pending(_ context.Context, _ string, vmid int) ([]string, error) {
	return f.pending[vmid], nil
}

func (f *fakeVolumes) RebootContainer(_ context.Context, _ string, vmid int) error {
	f.reboots = append(f.reboots, vmid)
	if !f.stuck {
		f.pending[vmid] = nil
	}
	return nil
}

// share is what the operator's pct set gives an LXC.
func (f *fakeVolumes) share(vmid int, key, source, path string, readOnly bool) {
	f.mps[vmid] = append(f.mps[vmid], proxmoxapi.MountPoint{Key: key, Volume: source, Path: path, ReadOnly: readOnly})
}

func mediaManifest() *manifest.Manifest {
	return &manifest.Manifest{
		APIVersion:   "urbex/v1",
		Project:      "media",
		Environments: []string{"prod"},
		Services: []manifest.Service{
			{Name: "vault", Image: "vaultwarden/server", Version: "1.34.3", Port: 80, Volumes: []manifest.Volume{
				{Name: "data", Path: "/data", Size: "5Gi"},
			}},
			{Name: "jellyfin", Image: "jellyfin/jellyfin", Version: "10.10.7", Port: 8096, Volumes: []manifest.Volume{
				{Name: "config", Path: "/config"},
				{Share: "media", Path: "/media", ReadOnly: true},
			}},
		},
	}
}

func volumesOrchestrator(t *testing.T) (*Orchestrator, *fakeVolumes, *fakeRollout, *strings.Builder) {
	t.Helper()
	o, ro, _, _, _ := imagesOrchestrator(mediaManifest())
	out := &strings.Builder{}
	o.Out = out
	o.Config.Storage.Shares = map[string]platform.Share{"media": {Path: "/mnt/pve/nas/media"}}
	vols := newFakeVolumes()
	o.Volumes = vols
	return o, vols, ro, out
}

func vmidOf(t *testing.T, o *Orchestrator, dir, service string) int {
	t.Helper()
	ledger, err := o.loadLedger(Options{GitOpsRepoPath: dir})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := ledger.Lookup(Hostname(o.Manifest.Project, service, "prod"))
	if !ok {
		t.Fatalf("%s has no allocation", service)
	}
	return entry.VMID
}

func TestApply_NamedVolumesBecomeMountPointsWithBackups(t *testing.T) {
	dir := t.TempDir()
	o, vols, ro, out := volumesOrchestrator(t)
	o.Manifest.Services = o.Manifest.Services[:1] // vault only
	ctx := context.Background()

	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v\n%s", err, out)
	}
	vmid := vmidOf(t, o, dir, "vault")
	want := []string{fmt.Sprintf("add %d mp0 local-lvm:5 /srv/volumes/vault/data", vmid)}
	if !slices.Equal(vols.calls, want) {
		t.Errorf("calls = %v, want %v", vols.calls, want)
	}
	if !slices.Equal(vols.reboots, []int{vmid}) {
		t.Errorf("expected the LXC restarted once to mount its new volume, got %v", vols.reboots)
	}
	if !slices.Contains(ro.deployed, "vault") {
		t.Errorf("vault should be deployed, got %v", ro.deployed)
	}

	// Again: nothing to add, nothing to restart.
	vols.calls, vols.reboots = nil, nil
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	if len(vols.calls) != 0 || len(vols.reboots) != 0 {
		t.Errorf("a re-apply should change nothing: calls %v, reboots %v", vols.calls, vols.reboots)
	}

	// Bigger: grown in place.
	o.Manifest.Services[0].Volumes[0].Size = "8Gi"
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply grown: %v", err)
	}
	if want := []string{fmt.Sprintf("resize %d mp0 8", vmid)}; !slices.Equal(vols.calls, want) || len(vols.reboots) != 0 {
		t.Errorf("calls = %v, reboots %v; want %v and no restart", vols.calls, vols.reboots, want)
	}

	// Smaller: refused, Proxmox can't shrink it.
	o.Manifest.Services[0].Volumes[0].Size = "2Gi"
	err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "can't shrink") {
		t.Errorf("expected a shrink refused, got %v", err)
	}
}

func TestApply_NoRestartWhenTheVolumeIsHotplugged(t *testing.T) {
	dir := t.TempDir()
	o, vols, _, _ := volumesOrchestrator(t)
	o.Manifest.Services = o.Manifest.Services[:1]
	vols.hotplugs = true
	if err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(vols.calls) != 1 || len(vols.reboots) != 0 {
		t.Errorf("calls %v, reboots %v", vols.calls, vols.reboots)
	}
}

func TestApply_SkipsApplicationsUntilTheirSharesAreMounted(t *testing.T) {
	dir := t.TempDir()
	o, vols, ro, out := volumesOrchestrator(t)
	ctx := context.Background()

	err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "not deployed until what they keep is mounted: jellyfin") {
		t.Fatalf("expected apply to fail naming jellyfin, got %v\n%s", err, out)
	}
	vmid := vmidOf(t, o, dir, "jellyfin")
	// Its named volume is mp0: the share takes the next free key.
	command := fmt.Sprintf("pct set %d -mp1 /mnt/pve/nas/media,mp=/srv/shares/media,ro=1", vmid)
	if !strings.Contains(out.String(), command) || !strings.Contains(out.String(), "root@pam") {
		t.Errorf("expected the operator's command %q:\n%s", command, out)
	}
	if !slices.Equal(ro.deployed, []string{"vault"}) || slices.ContainsFunc(ro.services, func(s manifest.Service) bool { return s.Name == "jellyfin" }) {
		t.Errorf("only vault should be set up and deployed: services %v, deployed %v", ro.services, ro.deployed)
	}
	if o.repoHas(dir, "jellyfin") {
		t.Error("jellyfin should have no folder in the GitOps repo yet")
	}

	// The operator runs the command: the next apply deploys it.
	vols.share(vmid, "mp1", "/mnt/pve/nas/media", "/srv/shares/media", true)
	vols.pending[vmid] = []string{"mp1"}
	vols.reboots = nil
	out.Reset()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply after the share: %v\n%s", err, out)
	}
	if !slices.Contains(ro.deployed, "jellyfin") || !o.repoHas(dir, "jellyfin") {
		t.Errorf("jellyfin should be deployed now, got %v", ro.deployed)
	}
	if !slices.Equal(vols.reboots, []int{vmid}) {
		t.Errorf("a share pending on a running LXC needs a restart, got %v", vols.reboots)
	}
}

func TestApply_RefusesAShareThePlatformDoesNotDeclare(t *testing.T) {
	o, _, _, _ := volumesOrchestrator(t)
	o.Config.Storage.Shares = nil
	err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "share media is not in storage.shares") {
		t.Errorf("expected the missing share refused, got %v", err)
	}
}

// A plan that replaces an LXC with named volumes would delete them.
func TestApply_RefusesReplacingAnLXCWithVolumes(t *testing.T) {
	dir := t.TempDir()
	o, vols, _, _ := volumesOrchestrator(t)
	host := Hostname("media", "vault", "prod")
	var ran []string
	o.Runner = runnerFunc(func(c runner.Command) {
		ran = append(ran, c.Args[0])
		if c.Args[0] == "show" && c.Stdout != nil {
			io.WriteString(c.Stdout, `{"resource_changes":[
				{"module_address":"module.service[\"`+host+`\"]","type":"proxmox_virtual_environment_container","index":null,"change":{"actions":["delete","create"],"replace_paths":[["unprivileged"]]}},
				{"module_address":"module.service[\"media-jellyfin-prod\"]","type":"proxmox_virtual_environment_container","change":{"actions":["update"]}}]}`)
		}
	})
	err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), host+" (volumes data; changed: unprivileged)") || strings.Contains(err.Error(), "jellyfin") {
		t.Fatalf("expected the replacement of vault refused, got %v", err)
	}
	if slices.Contains(ran, "apply") || len(vols.calls) != 0 {
		t.Errorf("nothing should be applied: ran %v, volume calls %v", ran, vols.calls)
	}
}

// A volume urbex.yaml no longer declares is still on the LXC, and
// would go with it.
func TestApply_RefusesReplacingAnLXCWithAVolumeNoLongerDeclared(t *testing.T) {
	dir := t.TempDir()
	o, _, _, _ := volumesOrchestrator(t)
	o.Manifest.Services = o.Manifest.Services[:1]
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	vmid, host := vmidOf(t, o, dir, "vault"), Hostname("media", "vault", "prod")
	o.Manifest.Services[0].Volumes = nil
	o.Proxmox = fakeLister{vmid}
	o.Runner = runnerFunc(func(c runner.Command) {
		if c.Args[0] == "show" && c.Stdout != nil {
			io.WriteString(c.Stdout, `{"resource_changes":[{"module_address":"module.service[\"`+host+`\"]","type":"proxmox_virtual_environment_container","change":{"actions":["delete","create"],"replace_paths":[["unprivileged"]]}}]}`)
		}
	})
	err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), host+" (volumes data;") {
		t.Fatalf("expected the replacement refused, got %v", err)
	}
}

// An application removed from urbex.yaml leaves the plan as a plain
// delete, and its volumes go with its LXC: apply refuses it, except
// with --delete-data.
func TestApply_RefusesRemovingAnApplicationWithVolumes(t *testing.T) {
	dir := t.TempDir()
	o, vols, _, out := volumesOrchestrator(t)
	o.Manifest.Services = o.Manifest.Services[:1]
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	vmid, host := vmidOf(t, o, dir, "vault"), Hostname("media", "vault", "prod")
	// urbex.yaml no longer has vault: only its LXC's mount points tell.
	o.Manifest.Services = []manifest.Service{{Name: "jellyfin", Image: "jellyfin/jellyfin", Version: "10.10.7", Port: 8096}}
	o.Proxmox = fakeLister{vmid}
	var ran []string
	o.Runner = runnerFunc(func(c runner.Command) {
		ran = append(ran, c.Args[0])
		if c.Args[0] == "show" && c.Stdout != nil {
			io.WriteString(c.Stdout, `{"resource_changes":[{"module_address":"module.service[\"`+host+`\"]","type":"proxmox_virtual_environment_container","change":{"actions":["delete"],"before":{"vm_id":`+strconv.Itoa(vmid)+`}}}]}`)
		}
	})
	err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), host+" (volumes vault/data)") || !strings.Contains(err.Error(), "urbex apply prod --delete-data") {
		t.Fatalf("expected the removal refused, saying how to do it, got %v", err)
	}
	if slices.Contains(ran, "apply") {
		t.Errorf("nothing should be applied: ran %v", ran)
	}

	out.Reset()
	_ = vols
	err = o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir, DeleteData: true})
	if strings.Contains(fmt.Sprint(err), "would delete") {
		t.Fatalf("--delete-data should let the removal through, got %v", err)
	}
	if !strings.Contains(out.String(), "Deleting "+host+" (volumes vault/data) with their LXCs.") {
		t.Errorf("expected the deletion named:\n%s", out)
	}
	if !slices.Contains(ran, "apply") {
		t.Errorf("terraform should apply, ran %v", ran)
	}
}

func TestDestroy_NamedVolumesNeedDeleteData(t *testing.T) {
	dir := t.TempDir()
	o, vols, _, out := volumesOrchestrator(t)
	o.Manifest.Services = o.Manifest.Services[:1]
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// A volume no longer in urbex.yaml still counts: it is on the LXC.
	vmid := vmidOf(t, o, dir, "vault")
	vols.mps[vmid] = append(vols.mps[vmid], proxmoxapi.MountPoint{Key: "mp1", Path: "/srv/volumes/vault/old", SizeGB: 1})
	o.Proxmox = fakeLister{vmid}

	r := &fakeRunner{}
	o.Runner = r
	err := o.Destroy(ctx, Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "vault/data, vault/old") || !strings.Contains(err.Error(), "--delete-data") {
		t.Fatalf("expected destroy refused naming the volumes, got %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("nothing should run: %v", r.calls)
	}

	if err := o.Destroy(ctx, Options{Env: "prod", GitOpsRepoPath: dir, DeleteData: true}); err != nil {
		t.Fatalf("Destroy --delete-data: %v", err)
	}
	if !strings.Contains(out.String(), "Deleting the named volumes of media (prod) with their LXCs: vault/data, vault/old.") {
		t.Errorf("expected the deletion named:\n%s", out)
	}
}

func TestDeletedContainers(t *testing.T) {
	// As terraform show -json writes it: the key is the module's.
	got, err := deletedContainers([]byte(`{"resource_changes":[
		{"module_address":"module.service[\"a-db-prod\"]","type":"proxmox_virtual_environment_container","index":null,"change":{"actions":["create","delete"],"replace_paths":[["disk",0,"datastore_id"],["unprivileged"]],"before":{"vm_id":9101}}},
		{"module_address":"module.service[\"a-api-prod\"]","type":"proxmox_virtual_environment_container","change":{"actions":["delete"],"before":{"vm_id":9102}}},
		{"module_address":"module.service[\"a-new-prod\"]","type":"proxmox_virtual_environment_container","change":{"actions":["create"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("deletedContainers = %+v", got)
	}
	if d := got[0]; d.hostname != "a-db-prod" || d.vmid != 9101 || !d.replaced || d.because != "disk.datastore_id, unprivileged" {
		t.Errorf("replacement = %+v", d)
	}
	if d := got[1]; d.hostname != "a-api-prod" || d.vmid != 9102 || d.replaced {
		t.Errorf("plain delete = %+v", d)
	}
	if got, err := deletedContainers(nil); err != nil || got != nil {
		t.Errorf("no output means no plan: %v %v", got, err)
	}
}

// repoHas reports whether the service has a folder in prod.
func (o *Orchestrator) repoHas(dir, service string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(envrepo.ServiceDir("prod", o.Manifest.Project, service))))
	return err == nil
}

// An apply interrupted between adding a mount point and restarting the
// LXC leaves it pending: the next apply restarts, and the application
// doesn't start before it is mounted.
func TestApply_RestartsForAVolumeAnEarlierApplyLeftPending(t *testing.T) {
	dir := t.TempDir()
	o, vols, _, out := volumesOrchestrator(t)
	o.Manifest.Services = o.Manifest.Services[:1]
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	vmid := vmidOf(t, o, dir, "vault")
	vols.pending[vmid] = []string{"mp0"} // as if the restart never happened
	vols.reboots = nil
	out.Reset()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply again: %v\n%s", err, out)
	}
	if !slices.Equal(vols.reboots, []int{vmid}) {
		t.Errorf("a volume still pending needs the LXC restarted, got %v", vols.reboots)
	}
	if !strings.Contains(out.String(), "Restarting media-vault-prod: Proxmox mounts mp0 only when its LXC restarts.") {
		t.Errorf("expected the restart said before it happens:\n%s", out)
	}
}

func TestApply_DoesNotStartAnApplicationWhoseVolumeIsNotMounted(t *testing.T) {
	dir := t.TempDir()
	o, vols, ro, out := volumesOrchestrator(t)
	o.Manifest.Services = o.Manifest.Services[:1]
	vols.stuck = true
	err := o.Apply(context.Background(), Options{Env: "prod", GitOpsRepoPath: dir})
	if err == nil || !strings.Contains(err.Error(), "not deployed until what they keep is mounted: vault") {
		t.Fatalf("expected apply to fail naming vault, got %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "the volumes mp0 of its LXC are not mounted yet") {
		t.Errorf("expected the reason:\n%s", out)
	}
	if slices.Contains(ro.deployed, "vault") || o.repoHas(dir, "vault") {
		t.Errorf("vault must not be deployed: %v", ro.deployed)
	}
}

// Applications that share an LXC (placement: stack) can each have a
// volume of the same name: the application is in the path.
func TestVolumePath_IncludesTheApplication(t *testing.T) {
	radarr := manifest.Service{Name: "radarr"}
	sonarr := manifest.Service{Name: "sonarr"}
	v := manifest.Volume{Name: "config", Path: "/config"}
	if got := radarr.LXCPath(v); got != "/srv/volumes/radarr/config" {
		t.Errorf("radarr: %s", got)
	}
	if radarr.LXCPath(v) == sonarr.LXCPath(v) {
		t.Error("two applications' volumes of the same name must not share a path")
	}
	if got := radarr.LXCPath(manifest.Volume{Share: "media", Path: "/media"}); got != "/srv/shares/media" {
		t.Errorf("share: %s", got)
	}
}

// The operator's pct set may spell the folder differently from
// urbex.platform.yaml: a trailing slash is the same folder.
func TestApply_ASharePathWithATrailingSlashIsTheSameFolder(t *testing.T) {
	dir := t.TempDir()
	o, vols, _, out := volumesOrchestrator(t)
	ctx := context.Background()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err == nil {
		t.Fatalf("expected apply to wait for the share\n%s", out)
	}
	vmid := vmidOf(t, o, dir, "jellyfin")
	vols.share(vmid, "mp1", "/mnt/pve/nas/media/", "/srv/shares/media", true)
	out.Reset()
	if err := o.Apply(ctx, Options{Env: "prod", GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Apply: %v\n%s", err, out)
	}
}
