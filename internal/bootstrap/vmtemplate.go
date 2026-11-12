package bootstrap

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/knownhosts"
	"github.com/urbforge/urbex-cli/internal/netalloc"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

// urbex's VM template (ADR-0033): a VM whose disk is imported from the
// operator's Debian 13 cloud image (proxmox.vmImage), with the same
// roles as the LXC template plus the QEMU guest agent. The applications
// with machine: vm are linked clones of it, configured by cloud-init.
// apply builds it the first time an application needs one, and again
// when what it holds changes.

// VMTemplateHostname is the VM template's name on Proxmox, and
// VMTemplateDiskGB the size of its disk: the least a VM's disk can be,
// since a linked clone can't be smaller than its template.
const (
	VMTemplateHostname = "urbex-vm-template"
	VMTemplateDiskGB   = 5
)

// vmTemplateVMIDs is the range of the VM templates' VMIDs, next to the
// LXC templates': each version gets its own, because Proxmox can't
// delete a template that VMs are linked clones of.
func vmTemplateVMIDs(cfg *platform.Config) (from, to int) {
	return cfg.Proxmox.VMIDBase + 80, cfg.Proxmox.VMIDBase + 89
}

// vmTemplateAddress is the address the VM template has while it is
// prepared: in the base services' range, before the LXC template's.
func vmTemplateAddress(cfg *platform.Config) (string, int, error) {
	addrs, prefix, err := netalloc.Allocate(cfg.Proxmox.Network.CIDR, cfg.Proxmox.Network.BaseHostOffset+8, 1)
	if err != nil {
		return "", 0, err
	}
	return addrs[0], prefix, nil
}

// vmTemplateFiles are the embedded files the VM template is built from.
var vmTemplateFiles = append([]string{"ansible/roles/vm-template"}, templateFiles...)

// VMTemplateVersion identifies what a VM template built now would hold.
func VMTemplateVersion(cfg *platform.Config) string {
	return templateHash(cfg.Proxmox.VMImage, vmTemplateFiles)
}

func vmTemplateDescription(version string) string {
	return "urbex VM template, version " + version + " - managed by urbex (ADR-0033)"
}

// VMTemplateProxmox is what building the VM template needs of Proxmox
// (proxmoxapi.Client).
type VMTemplateProxmox interface {
	VMTemplates(ctx context.Context, node string, from, to int) ([]int, error)
	VM(ctx context.Context, node string, vmid int) (*proxmoxapi.ContainerInfo, error)
	CreateVM(ctx context.Context, node string, spec proxmoxapi.VMSpec) error
	ResizeVMDisk(ctx context.Context, node string, vmid int, disk string, sizeGB int) error
	StartVM(ctx context.Context, node string, vmid int) error
	ShutdownVM(ctx context.Context, node string, vmid, timeout int) error
	StopVM(ctx context.Context, node string, vmid int) error
	ConvertVMToTemplate(ctx context.Context, node string, vmid int) error
	SetVMDescription(ctx context.Context, node string, vmid int, description string) error
	DeleteVM(ctx context.Context, node string, vmid int) error
}

// CurrentVMTemplate returns the VMID of the VM template holding the
// current version, or 0.
func CurrentVMTemplate(ctx context.Context, p VMTemplateProxmox, cfg *platform.Config) (int, error) {
	from, to := vmTemplateVMIDs(cfg)
	ids, err := p.VMTemplates(ctx, cfg.Proxmox.Node, from, to)
	if err != nil {
		return 0, err
	}
	want := "version " + VMTemplateVersion(cfg)
	for _, id := range ids {
		info, err := p.VM(ctx, cfg.Proxmox.Node, id)
		if err != nil {
			return 0, err
		}
		if info != nil && strings.Contains(info.Description, want) {
			return id, nil
		}
	}
	return 0, nil
}

// VMTemplateBuilder builds urbex's VM template: from apply, the first
// time an application asks for a VM.
type VMTemplateBuilder struct {
	Proxmox VMTemplateProxmox
	Config  *platform.Config
	Runner  CommandRunner
	Out     io.Writer
	// WaitSSH waits for a new VM to accept SSH connections; nil waits on
	// port 22 for up to five minutes.
	WaitSSH func(ctx context.Context, ip string) error
}

// Ensure returns the current VM template's VMID, building it first when
// there is none, and removes the older ones no VM is a clone of. Unlike
// the LXC template it is required: a VM has nothing else to start from.
func (b *VMTemplateBuilder) Ensure(ctx context.Context, gitopsRepoPath string) (int, error) {
	cfg := b.Config
	if cfg.Proxmox.VMImage == "" {
		return 0, fmt.Errorf("an application asks for a VM (machine: vm), and urbex.platform.yaml has no proxmox.vmImage: add the Debian 13 cloud image to a storage with the import content type, and name it there - see docs/reference/platform-config.md in urbforge/urbex")
	}
	node := cfg.Proxmox.Node
	current, err := CurrentVMTemplate(ctx, b.Proxmox, cfg)
	if err != nil {
		return 0, err
	}
	if current == 0 {
		if current, err = b.build(ctx, gitopsRepoPath); err != nil {
			return 0, fmt.Errorf("building the VM template: %w", err)
		}
	}
	from, to := vmTemplateVMIDs(cfg)
	ids, err := b.Proxmox.VMTemplates(ctx, node, from, to)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if id != current {
			if err := b.Proxmox.DeleteVM(ctx, node, id); err != nil {
				fmt.Fprintf(b.Out, "Kept the older VM template %d: VMs are still clones of it.\n", id)
			}
		}
	}
	return current, nil
}

// build builds the VM template at the first free VMID of the range,
// after removing what an interrupted build left there.
func (b *VMTemplateBuilder) build(ctx context.Context, gitopsRepoPath string) (int, error) {
	cfg, p := b.Config, b.Proxmox
	node, version := cfg.Proxmox.Node, VMTemplateVersion(cfg)
	from, to := vmTemplateVMIDs(cfg)
	vmid := 0
	for id := from; id <= to; id++ {
		info, err := p.VM(ctx, node, id)
		if err != nil {
			return 0, err
		}
		if info != nil && !info.Template {
			// A build that didn't finish: the range is the VM templates'.
			_ = p.StopVM(ctx, node, id)
			if err := p.DeleteVM(ctx, node, id); err != nil {
				return 0, fmt.Errorf("removing an unfinished VM template %d: %w", id, err)
			}
			info = nil
		}
		if info == nil && vmid == 0 {
			vmid = id
		}
	}
	if vmid == 0 {
		return 0, fmt.Errorf("no free VMID for a new VM template in %d-%d: older templates still have clones", from, to)
	}

	fmt.Fprintf(b.Out, "Preparing the VM template %s (version %s) from %s...\n", VMTemplateHostname, version, cfg.Proxmox.VMImage)
	ip, prefix, err := vmTemplateAddress(cfg)
	if err != nil {
		return 0, err
	}
	spec := proxmoxapi.VMSpec{
		VMID: vmid, Name: VMTemplateHostname, Image: cfg.Proxmox.VMImage,
		Storage: cfg.Proxmox.StoragePool, Pool: cfg.Proxmox.Pool,
		Bridge: cfg.Proxmox.Network.Bridge, IPCIDR: fmt.Sprintf("%s/%d", ip, prefix), Gateway: cfg.Proxmox.Network.Gateway,
		// The forwarders: the platform's DNS may not exist yet.
		Nameservers: cfg.Proxmox.Network.DNSServers, SSHPublicKey: cfg.Proxmox.SSHPublicKey,
	}
	if err := p.CreateVM(ctx, node, spec); err != nil {
		return 0, err
	}
	// The cloud image's disk is about 3 GB, too small for Docker's
	// images: cloud-init grows the root filesystem to the disk's size at
	// the first boot. Every VM is at least this big.
	if err := p.ResizeVMDisk(ctx, node, vmid, "scsi0", VMTemplateDiskGB); err != nil {
		return 0, err
	}
	if err := p.StartVM(ctx, node, vmid); err != nil {
		return 0, err
	}
	if err := knownhosts.Forget(gitopsRepoPath, ip); err != nil {
		return 0, err
	}
	wait := b.WaitSSH
	if wait == nil {
		wait = waitSSH
	}
	if err := wait(ctx, ip); err != nil {
		return 0, err
	}
	ansibleDir := filepath.Join(gitopsRepoPath, "ansible")
	if err := writeTemplateInventoryFor(ansibleDir, gitopsRepoPath, "urbex_vm_template", VMTemplateHostname, ip); err != nil {
		return 0, err
	}
	args := []string{"-i", templateInventory, "template-playbook.yml"}
	if err := b.Runner.Run(ctx, Command{Dir: ansibleDir, Name: "ansible-playbook", Args: args}); err != nil {
		return 0, fmt.Errorf("ansible-playbook template-playbook.yml: %w", err)
	}
	if err := knownhosts.Forget(gitopsRepoPath, ip); err != nil {
		return 0, err
	}
	if err := p.ShutdownVM(ctx, node, vmid, 120); err != nil {
		return 0, err
	}
	if err := p.ConvertVMToTemplate(ctx, node, vmid); err != nil {
		return 0, err
	}
	if err := p.SetVMDescription(ctx, node, vmid, vmTemplateDescription(version)); err != nil {
		return 0, err
	}
	fmt.Fprintf(b.Out, "VM template ready: new VMs are linked clones of %s (%d).\n", VMTemplateHostname, vmid)
	return vmid, nil
}

// waitSSH waits for port 22 at ip to accept connections: a VM boots and
// runs cloud-init before sshd listens.
func waitSSH(ctx context.Context, ip string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	addr := net.JoinHostPort(ip, "22")
	for {
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the VM at %s didn't accept SSH connections: %w", ip, err)
		case <-time.After(3 * time.Second):
		}
	}
}

// removeVMTemplate deletes the VM templates (and any unfinished build),
// on teardown: the VMs cloned from them are gone by then.
func (o *Orchestrator) removeVMTemplate(ctx context.Context) error {
	p, ok := o.Proxmox.(VMTemplateProxmox)
	if !ok {
		return nil
	}
	node := o.Config.Proxmox.Node
	from, to := vmTemplateVMIDs(o.Config)
	for id := from; id <= to; id++ {
		info, err := p.VM(ctx, node, id)
		if err != nil {
			return err
		}
		if info == nil {
			continue
		}
		if !info.Template {
			_ = p.StopVM(ctx, node, id)
		}
		if err := p.DeleteVM(ctx, node, id); err != nil {
			return err
		}
	}
	return nil
}
