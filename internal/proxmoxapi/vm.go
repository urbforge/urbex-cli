package proxmoxapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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

// --- the applications' named volumes in a VM (ADR-0033) --------------------

// VMDisk is one of a VM's SCSI disks: scsi0 is its root, the others the
// application's named volumes, told apart by their serial.
type VMDisk struct {
	Key    string // "scsi1"
	Volume string // "local-zfs:vm-9123-disk-1"
	Serial string // "web-data"
	SizeGB int
}

var vmDiskKey = regexp.MustCompile(`^scsi[0-9]+$`)

// Index is the number of the disk's key: 1 for scsi1.
func (d VMDisk) Index() int {
	n, _ := strconv.Atoi(strings.TrimPrefix(d.Key, "scsi"))
	return n
}

// VMDisks returns a VM's SCSI disks, in the order of their keys; CD
// drives (the cloud-init one) are left out.
func (c *Client) VMDisks(ctx context.Context, node string, vmid int) ([]VMDisk, error) {
	data, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), nil)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	var out []VMDisk
	for key, v := range cfg {
		s, ok := v.(string)
		if !ok || !vmDiskKey.MatchString(key) || strings.Contains(s, "media=cdrom") {
			continue
		}
		out = append(out, ParseVMDisk(key, s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index() < out[j].Index() })
	return out, nil
}

// ParseVMDisk reads a disk's value as Proxmox writes it, e.g.
// "local-zfs:vm-9123-disk-1,discard=on,iothread=1,serial=web-data,size=10G".
func ParseVMDisk(key, value string) VMDisk {
	d := VMDisk{Key: key}
	for _, p := range strings.Split(value, ",") {
		k, v, found := strings.Cut(p, "=")
		switch {
		case !found && d.Volume == "":
			d.Volume = p
		case k == "file":
			d.Volume = v
		case k == "serial":
			d.Serial = v
		case k == "size":
			d.SizeGB = sizeGB(v)
		}
	}
	return d
}

// AddVMDisk gives a running VM a new disk key of sizeGB on storage, with
// serial, included in its backups. Proxmox plugs it in at once (disk
// hotplug is on by default), and the VM finds it by its serial (lsblk's
// SERIAL).
func (c *Client) AddVMDisk(ctx context.Context, node string, vmid int, key, storage string, sizeGB int, serial string) error {
	form := url.Values{key: {fmt.Sprintf("%s:%d,serial=%s,discard=on,iothread=1,backup=1", storage, sizeGB, serial)}}
	_, err := c.call(ctx, http.MethodPut, fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), form)
	return err
}
