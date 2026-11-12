package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/urbforge/urbex-cli/internal/sizeparse"
)

// Manifest is the typed form of a validated urbex.yaml. See
// docs/reference/manifest.md in urbforge/urbex and
// schema/urbex.schema.json for the authoritative shape.
type Manifest struct {
	APIVersion string `yaml:"apiVersion"`
	Project    string `yaml:"project"`
	// Environments is nil when omitted: staging and prod (see
	// EnvironmentList).
	Environments  []string       `yaml:"environments,omitempty"`
	Frontend      *Frontend      `yaml:"frontend,omitempty"`
	Services      []Service      `yaml:"services,omitempty"`
	Domain        *Domain        `yaml:"domain,omitempty"`
	Observability *Observability `yaml:"observability,omitempty"`
	Email         *Email         `yaml:"email,omitempty"`
}

type Frontend struct {
	Type         string `yaml:"type"`
	Provider     string `yaml:"provider"`
	BuildCommand string `yaml:"buildCommand"`
	OutputDir    string `yaml:"outputDir"`
}

type Service struct {
	Name       string `yaml:"name"`
	Runtime    string `yaml:"runtime"`
	Dockerfile string `yaml:"dockerfile,omitempty"`
	// Image and Version, instead of Runtime, make an application from a
	// published image: pulled as it is, pinned by digest in each
	// environment (ADR-0028).
	Image   string `yaml:"image,omitempty"`
	Version string `yaml:"version,omitempty"`
	// Provides is the interface the application implements and Uses the
	// applications of the stack it needs (ADR-0028, ADR-0030).
	Provides string   `yaml:"provides,omitempty"`
	Uses     []string `yaml:"uses,omitempty"`
	// Volumes are what the application keeps: named volumes, mount
	// points of its LXC, and the operator's shares (ADR-0028).
	Volumes     []Volume `yaml:"volumes,omitempty"`
	Port        int      `yaml:"port"`
	Healthcheck string   `yaml:"healthcheck,omitempty"`
	// ReadyWithin is how long the application may take to be ready after
	// it starts ("90s", "5m"); "" is DefaultReadyWithin. See ReadySeconds.
	ReadyWithin string `yaml:"ready_within,omitempty"`
	// Public publishes the service on the internet, through the
	// platform's Cloudflare Tunnel (ADR-0022).
	Public    bool       `yaml:"public,omitempty"`
	Resources *Resources `yaml:"resources,omitempty"`
	// Machine is what the application runs on: MachineLXC (the default,
	// "") or MachineVM, for what an unprivileged LXC can't do - keyctl,
	// mount, fuse (ADR-0033).
	Machine string                       `yaml:"machine,omitempty"`
	Env     map[string]map[string]string `yaml:"env,omitempty"`
	Auth    *Auth                        `yaml:"auth,omitempty"`
	// Frontend is set on the service Deployables makes of a web
	// frontend; never read from urbex.yaml.
	Frontend *Frontend `yaml:"-"`
	// Extra is environment urbex gives the service in an environment
	// (e.g. its Keycloak realm's issuer); never read from urbex.yaml.
	Extra map[string]string `yaml:"-"`
}

// Default resource sizing applied when a service omits (or partially
// omits) its resources block - mirrors the defaults in
// schema/urbex.schema.json.
const (
	DefaultCPU    = 1
	DefaultMemory = "1Gi"
	DefaultDisk   = "5Gi"
)

// The machines an application runs on (ADR-0033).
const (
	MachineLXC = "lxc"
	MachineVM  = "vm"
	// DefaultVMDisk is a VM's disk when the application declares none:
	// the cloud image's root plus Docker's images. MinVMMemory is the
	// least memory a VM gets.
	DefaultVMDisk = "8Gi"
	MinVMMemory   = "1Gi"
	// MinVMDisk is the VM template's disk: a linked clone can't be
	// smaller.
	MinVMDisk = "5Gi"
)

// IsVM reports whether the application runs in a VM rather than an LXC.
func (s Service) IsVM() bool { return s.Machine == MachineVM }

// MachineKind is "VM" or "LXC", for messages.
func (s Service) MachineKind() string {
	if s.IsVM() {
		return "VM"
	}
	return "LXC"
}

// ResourcesOrDefault returns s.Resources with defaults filled in for any
// zero-value field, without mutating s. A VM's disk defaults to
// DefaultVMDisk.
func (s Service) ResourcesOrDefault() Resources {
	r := Resources{CPU: DefaultCPU, Memory: DefaultMemory, Disk: DefaultDisk}
	if s.IsVM() {
		r.Disk = DefaultVMDisk
	}
	if s.Resources == nil {
		return r
	}
	if s.Resources.CPU != 0 {
		r.CPU = s.Resources.CPU
	}
	if s.Resources.Memory != "" {
		r.Memory = s.Resources.Memory
	}
	if s.Resources.Disk != "" {
		r.Disk = s.Resources.Disk
	}
	return r
}

// EnvFor returns the non-secret environment variables declared for env
// ("staging" or "prod"), or nil if none are declared.
func (s Service) EnvFor(env string) map[string]string {
	if s.Env == nil {
		return nil
	}
	return s.Env[env]
}

// Volume is what an application keeps across redeploys: a named volume
// (Name, Size), or a share of the platform (Share, ReadOnly), mounted in
// the container at Path.
type Volume struct {
	Name     string `yaml:"name,omitempty"`
	Share    string `yaml:"share,omitempty"`
	Path     string `yaml:"path"`
	Size     string `yaml:"size,omitempty"`
	ReadOnly bool   `yaml:"readOnly,omitempty"`
}

// DefaultVolumeSize is a named volume's size when it declares none, as
// in schema/urbex.schema.json.
const DefaultVolumeSize = "10Gi"

// VolumesRoot and SharesRoot are where an application's LXC mounts its
// named volumes and shares: a named volume in a folder of its
// application and its name, a share in a folder of its name. The
// container gets a named volume as a Docker volume on its folder, and a
// share bound as it is. The application is in the path so
// that applications sharing an LXC (placement: stack) can each have a
// volume of the same name.
const (
	VolumesRoot = "/srv/volumes"
	SharesRoot  = "/srv/shares"
)

// LXCPath is where the volume v of the service is mounted on its LXC.
func (s Service) LXCPath(v Volume) string {
	if v.Share != "" {
		return SharesRoot + "/" + v.Share
	}
	return VolumesRoot + "/" + s.Name + "/" + v.Name
}

// MaxDiskSerial is the longest serial a Proxmox disk can have.
const MaxDiskSerial = 20

// DiskSerial is the serial of the disk a named volume is in a VM:
// <application>-<name>, which the VM finds it by whatever its order
// (ADR-0033). One longer than a serial may be keeps its first 11
// characters and a hash of the whole: still the same for the same
// volume, and unlike any other's.
func (s Service) DiskSerial(v Volume) string {
	serial := s.Name + "-" + v.Name
	if len(serial) <= MaxDiskSerial {
		return serial
	}
	sum := sha256.Sum256([]byte(serial))
	return serial[:11] + "-" + hex.EncodeToString(sum[:4])
}

// SizeOrDefault is a named volume's size.
func (v Volume) SizeOrDefault() string {
	if v.Size == "" {
		return DefaultVolumeSize
	}
	return v.Size
}

// NamedVolumes returns the service's named volumes, without its shares.
func (s Service) NamedVolumes() []Volume {
	var out []Volume
	for _, v := range s.Volumes {
		if v.Share == "" {
			out = append(out, v)
		}
	}
	return out
}

// Shares returns the service's shares, without its named volumes.
func (s Service) Shares() []Volume {
	var out []Volume
	for _, v := range s.Volumes {
		if v.Share != "" {
			out = append(out, v)
		}
	}
	return out
}

type Resources struct {
	CPU    int    `yaml:"cpu"`
	Memory string `yaml:"memory"`
	Disk   string `yaml:"disk"`
}

type Auth struct {
	Keycloak bool     `yaml:"keycloak"`
	Roles    []string `yaml:"roles"`
}

type Domain struct {
	Subdomain string `yaml:"subdomain"`
}

type Observability struct {
	Metrics bool `yaml:"metrics"`
	// Logs is nil when omitted: logs are shipped unless set to false.
	Logs *bool `yaml:"logs"`
}

// ShipsLogs reports whether the project's services send their logs to
// the platform's observability stack: yes, unless observability.logs is
// false.
func (m *Manifest) ShipsLogs() bool {
	return m.Observability == nil || m.Observability.Logs == nil || *m.Observability.Logs
}

type Email struct {
	Provider    string `yaml:"provider"`
	FromAddress string `yaml:"fromAddress"`
	FromName    string `yaml:"fromName,omitempty"`
}

// WebService is the name, and WebRuntime the runtime, of the service a
// web frontend is deployed as: built like the others, deployed to a
// Cloudflare Worker instead of an LXC (ADR-0022).
const (
	WebService = "web"
	WebRuntime = "web"
)

// IsWeb reports whether s is a web frontend.
func (s Service) IsWeb() bool { return s.Runtime == WebRuntime }

// FromImage reports whether s is an application from a published image,
// which urbex pulls rather than builds.
func (s Service) FromImage() bool { return s.Image != "" }

// Built returns the deployables built from the project's repository: a
// Komodo Build each, released and promoted by commit and release.
func Built(services []Service) []Service {
	var out []Service
	for _, svc := range services {
		if !svc.FromImage() {
			out = append(out, svc)
		}
	}
	return out
}

// FromImages returns the deployables from published images.
func FromImages(services []Service) []Service {
	var out []Service
	for _, svc := range services {
		if svc.FromImage() {
			out = append(out, svc)
		}
	}
	return out
}

// Service returns the deployable named name.
func (m *Manifest) Service(name string) (Service, bool) {
	for _, svc := range m.Deployables() {
		if svc.Name == name {
			return svc, true
		}
	}
	return Service{}, false
}

// EnvironmentList returns the stack's environments: what the manifest
// says, staging and prod by default.
func (m *Manifest) EnvironmentList() []string {
	if len(m.Environments) == 0 {
		return []string{"staging", "prod"}
	}
	return m.Environments
}

// HasEnvironment reports whether the stack has env.
func (m *Manifest) HasEnvironment(env string) bool {
	for _, e := range m.EnvironmentList() {
		if e == env {
			return true
		}
	}
	return false
}

// LeadingEnvironment is the environment a change goes to first, and the
// one urbex.yaml follows: staging, or prod for a stack that has only
// production.
func (m *Manifest) LeadingEnvironment() string {
	if m.HasEnvironment("staging") {
		return "staging"
	}
	return "prod"
}

// SingleEnvironment reports whether the stack has only production:
// release and promote don't apply to it.
func (m *Manifest) SingleEnvironment() bool { return !m.HasEnvironment("staging") }

// Unsupported returns what the manifest declares that the schema accepts
// but urbex doesn't act on yet - refused rather than ignored - what the
// links between applications get wrong, what an
// application from an image can't do without a port, and volumes that
// would be mounted twice.
func (m *Manifest) Unsupported() []string {
	var out []string
	for _, svc := range m.Services {
		names, paths := map[string]bool{}, map[string]bool{}
		for _, v := range svc.Volumes {
			key := "volume " + v.Name
			if v.Share != "" {
				key = "share " + v.Share
			}
			if names[key] {
				out = append(out, fmt.Sprintf("services %s: %s is declared twice", svc.Name, key))
			}
			if paths[v.Path] {
				out = append(out, fmt.Sprintf("services %s: two volumes are mounted at %s", svc.Name, v.Path))
			}
			names[key], paths[v.Path] = true, true
		}
		if svc.Port == 0 && svc.Public {
			out = append(out, fmt.Sprintf("services %s: public needs a port", svc.Name))
		}
		if svc.Port == 0 && svc.Healthcheck != "" {
			out = append(out, fmt.Sprintf("services %s: healthcheck needs a port", svc.Name))
		}
		if svc.IsVM() {
			out = append(out, svc.vmErrors()...)
		}
	}
	return append(out, m.linkErrors()...)
}

// Deployables returns what an environment of the project runs: its
// services and, if it has one, its web frontend.
func (m *Manifest) Deployables() []Service {
	out := append([]Service{}, m.Services...)
	if m.Frontend != nil && m.Frontend.Type == "web" {
		out = append(out, Service{Name: WebService, Runtime: WebRuntime, Frontend: m.Frontend})
	}
	return out
}

// vmErrors is what an application in a VM declares that a VM can't
// have, or doesn't have yet.
func (s Service) vmErrors() []string {
	var out []string
	if s.Resources != nil && s.Resources.Memory != "" {
		mb, err := sizeparse.MB(s.Resources.Memory)
		if minMB, _ := sizeparse.MB(MinVMMemory); err == nil && mb < minMB {
			out = append(out, fmt.Sprintf("services %s: a VM needs at least %s of memory, not %s", s.Name, MinVMMemory, s.Resources.Memory))
		}
	}
	if s.Resources != nil && s.Resources.Disk != "" {
		gb, err := sizeparse.GB(s.Resources.Disk)
		if minGB, _ := sizeparse.GB(MinVMDisk); err == nil && gb < minGB {
			out = append(out, fmt.Sprintf("services %s: a VM needs a disk of at least %s, not %s", s.Name, MinVMDisk, s.Resources.Disk))
		}
	}
	if len(s.Shares()) > 0 {
		// The VM mounts a share itself, over NFS or SMB: urbforge/urbex-cli#83.
		out = append(out, fmt.Sprintf("services %s: shares are not available in a VM yet (urbforge/urbex-cli#83): use machine: lxc", s.Name))
	}
	return out
}
