package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/urbforge/urbex-cli/internal/knownhosts"
	"github.com/urbforge/urbex-cli/internal/netalloc"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
	assets "github.com/urbforge/urbex-cli/platform"
)

// urbex's prepared LXC template (redemptor#18): the Debian template with
// what every LXC needs already installed - Docker, sops and the compose
// wrapper, the agents' images - built by the platform phase when missing
// or outdated. New LXCs are full clones of it; Ansible then only
// configures them.

// TemplateHostname is the template's name on Proxmox.
const TemplateHostname = "urbex-template"

// templateVMIDs is the range of the templates' VMIDs, in the base
// services' range, past them: each version of the template gets its own,
// because Proxmox can't delete a template that LXCs are linked clones of.
func templateVMIDs(cfg *platform.Config) (from, to int) {
	return cfg.Proxmox.VMIDBase + 90, cfg.Proxmox.VMIDBase + 99
}

// templateAddress is the address the template has while it is prepared:
// in the base services' range, past them (projects start at +10).
func templateAddress(cfg *platform.Config) (string, int, error) {
	addrs, prefix, err := netalloc.Allocate(cfg.Proxmox.Network.CIDR, cfg.Proxmox.Network.BaseHostOffset+9, 1)
	if err != nil {
		return "", 0, err
	}
	return addrs[0], prefix, nil
}

// templateFiles are the embedded files a template is built from: when one
// changes - a role, a pinned version - the template's version changes,
// and it is rebuilt.
var templateFiles = []string{
	"ansible/template-playbook.yml",
	"ansible/roles/common",
	"ansible/roles/sops-tools",
	"ansible/roles/template",
}

var composeImage = regexp.MustCompile(`(?m)^\s+image:\s*(\S+)\s*$`)

// alloyImage is the telemetry agent's image, as its Stack runs it.
func alloyImage() string {
	data, _ := assets.Stacks.ReadFile("stacks/telemetry/compose.yaml.tmpl")
	if m := composeImage.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// stackImages are the images of the platform Stack in platform/stacks/<dir>
// (none if there is no such Stack).
func stackImages(dir string) []string {
	data, err := assets.Stacks.ReadFile("stacks/" + dir + "/compose.yaml")
	if err != nil {
		return nil
	}
	var images []string
	for _, m := range composeImage.FindAllSubmatch(data, -1) {
		images = append(images, string(m[1]))
	}
	return images
}

// TemplateVersion identifies what a template built now would hold: the
// Debian template it starts from, the Komodo and agent versions, and the
// files that build it.
func TemplateVersion(cfg *platform.Config) string {
	h := sha256.New()
	fmt.Fprintln(h, cfg.Proxmox.LXCTemplate, KomodoVersion, alloyImage())
	var files []string
	for _, root := range templateFiles {
		fs.WalkDir(assets.Ansible, root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
	}
	sort.Strings(files)
	for _, f := range files {
		data, _ := assets.Ansible.ReadFile(f)
		fmt.Fprintln(h, f, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// templateDescription marks a template with the version it holds.
func templateDescription(version string) string {
	return "urbex LXC template, version " + version + " - managed by urbex (redemptor#18)"
}

// TemplateProxmox is what building the template needs of Proxmox
// (proxmoxapi.Client).
type TemplateProxmox interface {
	Templates(ctx context.Context, node string, from, to int) ([]int, error)
	Container(ctx context.Context, node string, vmid int) (*proxmoxapi.ContainerInfo, error)
	CreateContainer(ctx context.Context, node string, spec proxmoxapi.ContainerSpec) error
	StartContainer(ctx context.Context, node string, vmid int) error
	StopContainer(ctx context.Context, node string, vmid int) error
	ConvertToTemplate(ctx context.Context, node string, vmid int) error
	SetDescription(ctx context.Context, node string, vmid int, description string) error
	DeleteContainer(ctx context.Context, node string, vmid int) error
}

// CurrentTemplate returns the VMID new LXCs are cloned from: the
// template holding the current version, or 0 - create them from the
// Debian template.
func CurrentTemplate(ctx context.Context, p TemplateProxmox, cfg *platform.Config) (int, error) {
	from, to := templateVMIDs(cfg)
	ids, err := p.Templates(ctx, cfg.Proxmox.Node, from, to)
	if err != nil {
		return 0, err
	}
	want := "version " + TemplateVersion(cfg)
	for _, id := range ids {
		info, err := p.Container(ctx, cfg.Proxmox.Node, id)
		if err != nil {
			return 0, err
		}
		if info != nil && strings.Contains(info.Description, want) {
			return id, nil
		}
	}
	return 0, nil
}

// ensureTemplate returns the current template's VMID, building it first
// when there is none. The template only saves time: when Proxmox doesn't
// offer what building it needs, or building fails, the LXCs are created
// from the Debian template, as before, with a warning.
func (o *Orchestrator) ensureTemplate(ctx context.Context, gitopsRepoPath string) int {
	p, ok := o.Proxmox.(TemplateProxmox)
	if !ok {
		o.Progress.Skip(PhaseTemplate, "not available on this Proxmox")
		return 0
	}
	vmid, err := o.buildTemplate(ctx, p, gitopsRepoPath)
	if err != nil {
		o.Progress.Skip(PhaseTemplate, "couldn't build it: the LXCs come from the Debian template")
		fmt.Fprintf(o.Out, "Warning: couldn't prepare the LXC template (%v): new LXCs are created from %s and install everything themselves.\n", err, o.Config.Proxmox.LXCTemplate)
		return 0
	}
	return vmid
}

func (o *Orchestrator) buildTemplate(ctx context.Context, p TemplateProxmox, gitopsRepoPath string) (int, error) {
	cfg := o.Config
	node, version := cfg.Proxmox.Node, TemplateVersion(cfg)
	current, err := CurrentTemplate(ctx, p, cfg)
	if err != nil {
		return 0, err
	}
	if current != 0 {
		o.Progress.Skip(PhaseTemplate, "the current one is there")
	} else {
		o.Progress.Begin(PhaseTemplate)
		if current, err = o.newTemplate(ctx, p, gitopsRepoPath, version); err != nil {
			return 0, err
		}
	}
	// Older templates go once no LXC is a clone of them.
	from, to := templateVMIDs(cfg)
	ids, err := p.Templates(ctx, node, from, to)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if id != current {
			if err := p.DeleteContainer(ctx, node, id); err != nil {
				fmt.Fprintf(o.Out, "Kept the older LXC template %d: LXCs are still clones of it.\n", id)
			}
		}
	}
	return current, nil
}

// newTemplate builds the template at the first free VMID of the range,
// after removing what an interrupted build left there.
func (o *Orchestrator) newTemplate(ctx context.Context, p TemplateProxmox, gitopsRepoPath, version string) (int, error) {
	cfg := o.Config
	node := cfg.Proxmox.Node
	from, to := templateVMIDs(cfg)
	vmid := 0
	for id := from; id <= to; id++ {
		info, err := p.Container(ctx, node, id)
		if err != nil {
			return 0, err
		}
		if info != nil && !info.Template {
			// A build that didn't finish: the range is the templates'.
			_ = p.StopContainer(ctx, node, id)
			if err := p.DeleteContainer(ctx, node, id); err != nil {
				return 0, fmt.Errorf("removing an unfinished template %d: %w", id, err)
			}
			info = nil
		}
		if info == nil && vmid == 0 {
			vmid = id
		}
	}
	if vmid == 0 {
		return 0, fmt.Errorf("no free VMID for a new template in %d-%d: older templates still have clones", from, to)
	}

	fmt.Fprintf(o.Out, "Preparing the LXC template %s (version %s)...\n", TemplateHostname, version)
	ip, prefix, err := templateAddress(cfg)
	if err != nil {
		return 0, err
	}
	spec := proxmoxapi.ContainerSpec{
		VMID: vmid, Hostname: TemplateHostname, OSTemplate: cfg.Proxmox.LXCTemplate,
		Storage: cfg.Proxmox.StoragePool, DiskGB: 4, Pool: cfg.Proxmox.Pool,
		Bridge: cfg.Proxmox.Network.Bridge, IPCIDR: fmt.Sprintf("%s/%d", ip, prefix), Gateway: cfg.Proxmox.Network.Gateway,
		// The forwarders: the platform's DNS may not exist yet.
		Nameservers: cfg.Proxmox.Network.DNSServers, SSHPublicKey: cfg.Proxmox.SSHPublicKey, Keyctl: cfg.Proxmox.KeyctlEnabled(),
	}
	if err := p.CreateContainer(ctx, node, spec); err != nil {
		return 0, err
	}
	if err := p.StartContainer(ctx, node, vmid); err != nil {
		return 0, err
	}
	if err := knownhosts.Forget(gitopsRepoPath, ip); err != nil {
		return 0, err
	}
	ansibleDir := filepath.Join(gitopsRepoPath, "ansible")
	if err := writeTemplateInventory(ansibleDir, gitopsRepoPath, ip); err != nil {
		return 0, err
	}
	args := []string{"-i", templateInventory, "template-playbook.yml"}
	if err := o.Runner.Run(ctx, Command{Dir: ansibleDir, Name: "ansible-playbook", Args: args}); err != nil {
		return 0, fmt.Errorf("ansible-playbook template-playbook.yml: %w", err)
	}
	// The address is the template's no more: its next owner has another key.
	if err := knownhosts.Forget(gitopsRepoPath, ip); err != nil {
		return 0, err
	}
	if err := p.StopContainer(ctx, node, vmid); err != nil {
		return 0, err
	}
	if err := p.ConvertToTemplate(ctx, node, vmid); err != nil {
		return 0, err
	}
	if err := p.SetDescription(ctx, node, vmid, templateDescription(version)); err != nil {
		return 0, err
	}
	fmt.Fprintf(o.Out, "LXC template ready: new LXCs are linked clones of %s (%d).\n", TemplateHostname, vmid)
	return vmid, nil
}

const templateInventory = "template-inventory.yml"

func writeTemplateInventory(ansibleDir, repoPath, ip string) error {
	sshArgs, err := knownhosts.SSHArgs(repoPath)
	if err != nil {
		return err
	}
	inv := map[string]any{"all": map[string]any{
		"vars": map[string]any{
			"ansible_user": "root", "ansible_ssh_common_args": sshArgs,
			"ansible_timeout": knownhosts.ConnectTimeout, "ansible_ssh_retries": knownhosts.ConnectRetries,
			"komodo_version": KomodoVersion, "alloy_image": alloyImage(),
		},
		"children": map[string]any{"urbex_template": map[string]any{
			"hosts": map[string]any{TemplateHostname: map[string]any{"ansible_host": ip}},
		}},
	}}
	data, err := yaml.Marshal(inv)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ansibleDir, path.Clean(templateInventory)), data, 0o644)
}

// removeTemplate deletes the templates (and any unfinished build), on
// teardown: the LXCs cloned from them are gone by then.
func (o *Orchestrator) removeTemplate(ctx context.Context) error {
	p, ok := o.Proxmox.(TemplateProxmox)
	if !ok {
		return nil
	}
	node := o.Config.Proxmox.Node
	from, to := templateVMIDs(o.Config)
	for id := from; id <= to; id++ {
		info, err := p.Container(ctx, node, id)
		if err != nil {
			return err
		}
		if info == nil {
			continue
		}
		if !info.Template {
			_ = p.StopContainer(ctx, node, id)
		}
		if err := p.DeleteContainer(ctx, node, id); err != nil {
			return err
		}
	}
	return nil
}
