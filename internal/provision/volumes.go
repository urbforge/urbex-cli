package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
	"github.com/urbforge/urbex-cli/internal/runner"
	"github.com/urbforge/urbex-cli/internal/sizeparse"
)

// VolumeAPI is the subset of proxmoxapi.Client that gives the
// applications' LXCs their volumes (ADR-0028). Urbex adds and grows them
// through the API rather than Terraform: the provider replaces an LXC on
// any change to its mount points, and Proxmox deletes the volumes with
// it.
type VolumeAPI interface {
	MountPoints(ctx context.Context, node string, vmid int) ([]proxmoxapi.MountPoint, error)
	AddVolume(ctx context.Context, node string, vmid int, key, storage string, sizeGB int, path string) error
	ResizeVolume(ctx context.Context, node string, vmid int, key string, sizeGB int) error
	Pending(ctx context.Context, node string, vmid int) ([]string, error)
	RebootContainer(ctx context.Context, node string, vmid int) error
}

// maxMountPoints is how many mount points an LXC can have: mp0 to mp255.
const maxMountPoints = 256

// checkVolumes refuses a share the platform doesn't declare.
func (o *Orchestrator) checkVolumes() error {
	var errs []string
	for _, svc := range o.Manifest.Services {
		for _, v := range svc.Shares() {
			if _, ok := o.Config.Storage.Shares[v.Share]; !ok {
				errs = append(errs, fmt.Sprintf("services %s: share %s is not in storage.shares of urbex.platform.yaml", svc.Name, v.Share))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// namedVolumes returns, by hostname, the names of the named volumes of
// the services' LXCs in env: those urbex.yaml declares, and those an
// existing LXC still has, which urbex.yaml may no longer declare.
func (o *Orchestrator) namedVolumes(ctx context.Context, env string, services []manifest.Service, allocations map[string]allocation.Entry, existing map[int]bool) (map[string][]string, error) {
	out := map[string][]string{}
	for _, svc := range services {
		names := map[string]bool{}
		for _, v := range svc.NamedVolumes() {
			names[v.Name] = true
		}
		hostname := Hostname(o.Manifest.Project, svc.Name, env)
		if vmid := allocations[hostname].VMID; o.Volumes != nil && existing[vmid] {
			mps, err := o.Volumes.MountPoints(ctx, o.Config.Proxmox.Node, vmid)
			if err != nil {
				return nil, fmt.Errorf("reading the mount points of %s: %w", hostname, err)
			}
			for _, mp := range mps {
				if name, ok := strings.CutPrefix(mp.Path, manifest.VolumesRoot+"/"+svc.Name+"/"); ok {
					names[name] = true
				}
			}
		}
		if len(names) > 0 {
			out[hostname] = slices.Sorted(maps.Keys(names))
		}
	}
	return out, nil
}

// refuseReplacingVolumes runs terraform plan and refuses a plan that
// would replace an LXC with named volumes, by hostname: Proxmox deletes
// the volumes with their LXC. The plan is kept out of the GitOps repo -
// it holds the variables, the API token among them - and removed
// afterwards.
func (o *Orchestrator) refuseReplacingVolumes(ctx context.Context, tfDir string, tfEnv []string, volumes map[string][]string) error {
	if len(volumes) == 0 {
		return nil
	}
	tmp, err := os.MkdirTemp("", "urbex-plan-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	plan := filepath.Join(tmp, "volumes.tfplan")
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"plan", "-input=false", "-out=" + plan}, Env: tfEnv}); err != nil {
		return fmt.Errorf("terraform plan: %w", err)
	}
	var shown bytes.Buffer
	if err := o.Runner.Run(ctx, runner.Command{Dir: tfDir, Name: "terraform", Args: []string{"show", "-json", plan}, Env: tfEnv, Stdout: &shown}); err != nil {
		return fmt.Errorf("terraform show: %w", err)
	}
	replaced, err := replacedContainers(shown.Bytes())
	if err != nil {
		return err
	}
	var refused []string
	for _, r := range replaced {
		if names, ok := volumes[r.hostname]; ok {
			refused = append(refused, fmt.Sprintf("%s (volumes %s; changed: %s)", r.hostname, strings.Join(names, ", "), r.because))
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("the change would replace %s, and Proxmox deletes an LXC's volumes with it: revert the change, or move the data out first (see known limitations)", strings.Join(refused, "; "))
	}
	return nil
}

type replacement struct {
	hostname string
	because  string
}

// replacedContainers reads, in terraform show -json of a plan, the LXCs
// the plan would delete and create again, by hostname, with the
// attributes that make it replace them. The hostname is the key of the
// module instance (module.service["<hostname>"]): the container itself
// has no key.
func replacedContainers(plan []byte) ([]replacement, error) {
	if len(bytes.TrimSpace(plan)) == 0 {
		return nil, nil
	}
	var p struct {
		ResourceChanges []struct {
			Type          string `json:"type"`
			ModuleAddress string `json:"module_address"`
			Change        struct {
				Actions      []string `json:"actions"`
				ReplacePaths [][]any  `json:"replace_paths"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(plan, &p); err != nil {
		return nil, fmt.Errorf("reading terraform plan: %w", err)
	}
	var out []replacement
	for _, rc := range p.ResourceChanges {
		key, ok := strings.CutPrefix(rc.ModuleAddress, "module.service[")
		if rc.Type != "proxmox_virtual_environment_container" || !ok {
			continue
		}
		host, err := strconv.Unquote(strings.TrimSuffix(key, "]"))
		if err != nil {
			continue
		}
		if !slices.Contains(rc.Change.Actions, "delete") || !slices.Contains(rc.Change.Actions, "create") {
			continue
		}
		var paths []string
		for _, path := range rc.Change.ReplacePaths {
			var parts []string
			for _, step := range path {
				if s, ok := step.(string); ok {
					parts = append(parts, s)
				}
			}
			paths = append(paths, strings.Join(parts, "."))
		}
		because := strings.Join(paths, ", ")
		if because == "" {
			because = "see terraform plan"
		}
		out = append(out, replacement{hostname: host, because: because})
	}
	return out, nil
}

// notReady is an application apply leaves out because its LXC doesn't
// have everything it keeps yet: a share the operator hasn't bound, with
// the command that does it, or named volumes that stay unmounted.
type notReady struct {
	service string
	command string // the operator's pct set, for a share
	volumes string // the named volumes still unmounted
}

// mountVolumes gives each application's LXC the volumes urbex.yaml
// declares: a named volume it doesn't have yet becomes a new mount
// point, one smaller than declared grows; a running LXC that can't take
// a new mount point at once restarts. An application whose volumes are
// still not mounted after that is not ready, and neither is one with a
// share the operator hasn't bound: it returns them, with the pct command
// for each share.
func (o *Orchestrator) mountVolumes(ctx context.Context, env string, allocations map[string]allocation.Entry) ([]notReady, error) {
	node := o.Config.Proxmox.Node
	var missing []notReady
	for _, svc := range o.Manifest.Services {
		if len(svc.Volumes) == 0 {
			continue
		}
		hostname := Hostname(o.Manifest.Project, svc.Name, env)
		vmid := allocations[hostname].VMID
		mps, err := o.Volumes.MountPoints(ctx, node, vmid)
		if err != nil {
			return nil, fmt.Errorf("reading the mount points of %s: %w", hostname, err)
		}
		at := map[string]proxmoxapi.MountPoint{}
		taken := map[int]bool{}
		for _, mp := range mps {
			at[mp.Path] = mp
			taken[mp.Index()] = true
		}
		free := func() (string, error) {
			for i := 0; i < maxMountPoints; i++ {
				if !taken[i] {
					taken[i] = true
					return fmt.Sprintf("mp%d", i), nil
				}
			}
			return "", fmt.Errorf("%s has no free mount point left", hostname)
		}

		// mounted are the keys this LXC must have mounted for the
		// application to start: its named volumes, and its shares once
		// the operator has bound them.
		var mounted []string
		for _, v := range svc.NamedVolumes() {
			want, err := sizeparse.GB(v.SizeOrDefault())
			if err != nil {
				return nil, fmt.Errorf("services %s: volume %s: %w", svc.Name, v.Name, err)
			}
			mp, ok := at[svc.LXCPath(v)]
			switch {
			case !ok:
				key, err := free()
				if err != nil {
					return nil, err
				}
				if err := o.Volumes.AddVolume(ctx, node, vmid, key, o.Config.Proxmox.StoragePool, want, svc.LXCPath(v)); err != nil {
					return nil, fmt.Errorf("adding volume %s to %s: %w", v.Name, hostname, err)
				}
				mounted = append(mounted, key)
				fmt.Fprintf(o.Out, "%s: volume %s, %d GB, mounted at %s (%s).\n", svc.Name, v.Name, want, v.Path, key)
			case mp.SizeGB < want:
				if err := o.Volumes.ResizeVolume(ctx, node, vmid, mp.Key, want); err != nil {
					return nil, fmt.Errorf("growing volume %s of %s: %w", v.Name, hostname, err)
				}
				fmt.Fprintf(o.Out, "%s: volume %s grown from %d to %d GB.\n", svc.Name, v.Name, mp.SizeGB, want)
				mounted = append(mounted, mp.Key)
			case mp.SizeGB > want:
				return nil, fmt.Errorf("services %s: volume %s is %d GB and urbex.yaml asks for %d: Proxmox can't shrink a volume, set its size to %dGi or more", svc.Name, v.Name, mp.SizeGB, want, mp.SizeGB)
			default:
				mounted = append(mounted, mp.Key)
			}
		}
		for _, v := range svc.Shares() {
			source := o.Config.Storage.Shares[v.Share].Path
			mp, ok := at[svc.LXCPath(v)]
			if ok && path.Clean(mp.Volume) == path.Clean(source) && mp.ReadOnly == v.ReadOnly {
				mounted = append(mounted, mp.Key)
				continue
			}
			key := mp.Key
			if !ok {
				if key, err = free(); err != nil {
					return nil, err
				}
			}
			command := fmt.Sprintf("pct set %d -%s %s,mp=%s", vmid, key, source, svc.LXCPath(v))
			if v.ReadOnly {
				command += ",ro=1"
			}
			missing = append(missing, notReady{service: svc.Name, command: command})
		}
		if len(mounted) == 0 {
			continue
		}
		// A mount point Proxmox applies only when the LXC restarts is not
		// mounted yet. This apply may have added it, or an earlier one
		// that was interrupted before the restart. The application must
		// not start before it is mounted.
		unmounted := func() ([]string, error) {
			pending, err := o.Volumes.Pending(ctx, node, vmid)
			if err != nil {
				return nil, fmt.Errorf("reading the pending changes of %s: %w", hostname, err)
			}
			return slices.DeleteFunc(slices.Clone(mounted), func(k string) bool { return !slices.Contains(pending, k) }), nil
		}
		keys, err := unmounted()
		if err != nil {
			return nil, err
		}
		if len(keys) > 0 {
			fmt.Fprintf(o.Out, "Restarting %s: Proxmox mounts %s only when its LXC restarts.\n", hostname, strings.Join(keys, ", "))
			if err := o.Volumes.RebootContainer(ctx, node, vmid); err != nil {
				return nil, fmt.Errorf("restarting %s: %w", hostname, err)
			}
			if keys, err = unmounted(); err != nil {
				return nil, err
			}
		}
		if len(keys) > 0 {
			missing = append(missing, notReady{service: svc.Name, volumes: strings.Join(keys, ", ")})
		}
	}
	return missing, nil
}

// reportNotReady names the applications apply skipped, with the
// operator's commands for their shares, and fails: the environment isn't
// complete. An application never starts without what it keeps.
func (o *Orchestrator) reportNotReady(env string, missing []notReady) error {
	services := map[string]bool{}
	first := true
	for _, m := range missing {
		services[m.service] = true
		if m.command == "" {
			fmt.Fprintf(o.Out, "%s: the volumes %s of its LXC are not mounted yet, and a container started now would write to the LXC's own disk.\n", m.service, m.volumes)
			continue
		}
		if first {
			fmt.Fprintf(o.Out, "Some applications need shares their LXC doesn't have yet. Binding a folder of the Proxmox node needs root@pam: on the node, as root, run\n")
			first = false
		}
		fmt.Fprintf(o.Out, "  %s\n", m.command)
	}
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintf(o.Out, "Then run 'urbex apply %s' again.\n", env)
	return fmt.Errorf("not deployed until what they keep is mounted: %s", strings.Join(names, ", "))
}

// checkDeleteData refuses to destroy LXCs with named volumes unless
// opts.DeleteData says to: their volumes go with them. Shares are never
// touched.
func (o *Orchestrator) checkDeleteData(ctx context.Context, opts Options, services []manifest.Service, allocations map[string]allocation.Entry) error {
	existing, err := o.existingContainers(ctx)
	if err != nil {
		return err
	}
	volumes, err := o.namedVolumes(ctx, opts.Env, services, allocations, existing)
	if err != nil {
		return err
	}
	var keeping []string
	for _, svc := range services {
		for _, name := range volumes[Hostname(o.Manifest.Project, svc.Name, opts.Env)] {
			keeping = append(keeping, svc.Name+"/"+name)
		}
	}
	if len(keeping) == 0 {
		return nil
	}
	if !opts.DeleteData {
		return fmt.Errorf("%s (%s) has named volumes: %s. Destroying it deletes them with their LXCs: run 'urbex destroy %s --delete-data' to do so", o.Manifest.Project, opts.Env, strings.Join(keeping, ", "), opts.Env)
	}
	fmt.Fprintf(o.Out, "Deleting the named volumes of %s (%s) with their LXCs: %s.\n", o.Manifest.Project, opts.Env, strings.Join(keeping, ", "))
	return nil
}
