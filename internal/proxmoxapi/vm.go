package proxmoxapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// --- urbex's VM template (ADR-0033) ----------------------------------------

// VMSpec is the VM urbex's VM template is built from: its disk imported
// from a cloud image, cloud-init for the network and root's SSH key, in
// a pool.
type VMSpec struct {
	VMID         int
	Name         string
	Image        string // e.g. local:import/debian-13-genericcloud-amd64.qcow2
	Storage      string
	Pool         string
	Bridge       string
	IPCIDR       string // e.g. 192.168.1.208/24
	Gateway      string
	Nameservers  []string
	SSHPublicKey string
}

// CreateVM creates the VM spec describes and waits for it. The token
// needs no more than on its pool: import-from reads a volume of content
// type import with Datastore.AllocateSpace or Datastore.Audit on its
// storage (checked on the Cubotto lab, urbforge/urbex-cli#82).
func (c *Client) CreateVM(ctx context.Context, node string, spec VMSpec) error {
	form := url.Values{}
	form.Set("vmid", fmt.Sprint(spec.VMID))
	form.Set("name", spec.Name)
	form.Set("ostype", "l26")
	form.Set("cores", "1")
	form.Set("memory", "1024")
	form.Set("scsihw", "virtio-scsi-single")
	form.Set("scsi0", fmt.Sprintf("%s:0,import-from=%s,discard=on", spec.Storage, spec.Image))
	form.Set("boot", "order=scsi0")
	form.Set("ide2", spec.Storage+":cloudinit")
	form.Set("agent", "enabled=1")
	form.Set("net0", "virtio,bridge="+spec.Bridge)
	form.Set("ciuser", "root")
	// Proxmox wants the keys URL-encoded, and the form encodes them again.
	form.Set("sshkeys", strings.ReplaceAll(url.QueryEscape(spec.SSHPublicKey), "+", "%20"))
	form.Set("ipconfig0", fmt.Sprintf("ip=%s,gw=%s", spec.IPCIDR, spec.Gateway))
	if len(spec.Nameservers) > 0 {
		form.Set("nameserver", strings.Join(spec.Nameservers, " "))
	}
	// No package upgrade at first boot: Ansible installs what it needs,
	// and the clones would each upgrade again.
	form.Set("ciupgrade", "0")
	if spec.Pool != "" {
		form.Set("pool", spec.Pool)
	}
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu", node), form)
}

// ResizeVMDisk grows a VM's disk (scsi0, ...) to sizeGB, and waits.
func (c *Client) ResizeVMDisk(ctx context.Context, node string, vmid int, disk string, sizeGB int) error {
	form := url.Values{"disk": {disk}, "size": {fmt.Sprintf("%dG", sizeGB)}}
	return c.task(ctx, node, http.MethodPut, fmt.Sprintf("/nodes/%s/qemu/%d/resize", node, vmid), form)
}

// VM returns a VM's configuration, or nil when there is no VM vmid on
// node.
func (c *Client) VM(ctx context.Context, node string, vmid int) (*ContainerInfo, error) {
	return c.machine(ctx, node, "qemu", vmid)
}

// VMTemplates returns the VMIDs of the node's VM templates in [from, to].
func (c *Client) VMTemplates(ctx context.Context, node string, from, to int) ([]int, error) {
	return c.templates(ctx, node, "qemu", from, to)
}

// StartVM starts a VM and waits for it.
func (c *Client) StartVM(ctx context.Context, node string, vmid int) error {
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/status/start", node, vmid), nil)
}

// ShutdownVM shuts a VM down cleanly (ACPI), stopping it if it hasn't
// after timeout seconds, and waits.
func (c *Client) ShutdownVM(ctx context.Context, node string, vmid, timeout int) error {
	form := url.Values{"timeout": {fmt.Sprint(timeout)}, "forceStop": {"1"}}
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/status/shutdown", node, vmid), form)
}

// StopVM stops a VM at once and waits.
func (c *Client) StopVM(ctx context.Context, node string, vmid int) error {
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/status/stop", node, vmid), nil)
}

// ConvertVMToTemplate turns a stopped VM into a template, the source of
// linked clones.
func (c *Client) ConvertVMToTemplate(ctx context.Context, node string, vmid int) error {
	return c.task(ctx, node, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/template", node, vmid), nil)
}

// SetVMDescription sets a VM's (or VM template's) description.
func (c *Client) SetVMDescription(ctx context.Context, node string, vmid int, description string) error {
	form := url.Values{"description": {description}}
	_, err := c.call(ctx, http.MethodPut, fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), form)
	return err
}

// DeleteVM destroys a VM (or VM template) and its disks, and waits.
func (c *Client) DeleteVM(ctx context.Context, node string, vmid int) error {
	return c.task(ctx, node, http.MethodDelete, fmt.Sprintf("/nodes/%s/qemu/%d?destroy-unreferenced-disks=1&purge=1", node, vmid), nil)
}
