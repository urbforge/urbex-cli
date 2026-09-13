package manifest

// Manifest is the typed form of a validated urbex.yaml. See
// docs/manifest-spec.md in urbforge/urbex and
// schema/urbex.schema.json for the authoritative shape.
type Manifest struct {
	APIVersion    string         `yaml:"apiVersion"`
	Project       string         `yaml:"project"`
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
	Name        string                       `yaml:"name"`
	Runtime     string                       `yaml:"runtime"`
	Dockerfile  string                       `yaml:"dockerfile,omitempty"`
	Port        int                          `yaml:"port"`
	Healthcheck string                       `yaml:"healthcheck,omitempty"`
	Resources   *Resources                   `yaml:"resources,omitempty"`
	Env         map[string]map[string]string `yaml:"env,omitempty"`
	Auth        *Auth                        `yaml:"auth,omitempty"`
}

// Default resource sizing applied when a service omits (or partially
// omits) its resources block - mirrors the defaults in
// schema/urbex.schema.json.
const (
	DefaultCPU    = 1
	DefaultMemory = "1Gi"
	DefaultDisk   = "5Gi"
)

// ResourcesOrDefault returns s.Resources with defaults filled in for any
// zero-value field, without mutating s.
func (s Service) ResourcesOrDefault() Resources {
	r := Resources{CPU: DefaultCPU, Memory: DefaultMemory, Disk: DefaultDisk}
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
	Logs    bool `yaml:"logs"`
}

type Email struct {
	Provider    string `yaml:"provider"`
	FromAddress string `yaml:"fromAddress"`
	FromName    string `yaml:"fromName,omitempty"`
}
