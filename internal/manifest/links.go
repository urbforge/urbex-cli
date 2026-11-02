package manifest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// InterfacePostgres is the interface of a PostgreSQL server (ADR-0030):
// the only one urbex configures today (ADR-0028 lists mysql and redis as
// later ones).
const InterfacePostgres = "postgres"

// Keys an application exports to the ones that use it, whatever its
// interface, and the extra ones a credentialed interface adds
// (ADR-0028, Stacks: how applications find each other).
var (
	plainKeys      = []string{"host", "port", "url"}
	credentialKeys = []string{"user", "password", "database"}
)

// InterfaceKeys are the values an application of the interface exports,
// for `${app.key}` references in env. An application with no interface
// ("") exports host, port and url.
func InterfaceKeys(iface string) []string {
	if iface == InterfacePostgres {
		return append(append([]string{}, plainKeys...), credentialKeys...)
	}
	return append([]string{}, plainKeys...)
}

// Reference is a `${app.key}` in an env value.
type Reference struct {
	App, Key string
}

var referenceRE = regexp.MustCompile(`\$\{([a-z][a-z0-9-]*)\.([a-z]+)\}`)

// References returns the `${app.key}` references in value.
func References(value string) []Reference {
	var out []Reference
	for _, m := range referenceRE.FindAllStringSubmatch(value, -1) {
		out = append(out, Reference{App: m[1], Key: m[2]})
	}
	return out
}

// service returns the application named name among the stack's services.
func (m *Manifest) service(name string) (Service, bool) {
	for _, svc := range m.Services {
		if svc.Name == name {
			return svc, true
		}
	}
	return Service{}, false
}

// Interface is what the service provides, "" for an application with no
// interface.
func (s Service) Interface() string { return s.Provides }

// linkErrors are what `uses` and the env references of the stack's
// services get wrong: an application that isn't there, one that uses
// itself, one used with no way to tell it is ready (neither an interface
// nor a healthcheck), a cycle, a reference to something the service
// doesn't use or to a value its interface doesn't export.
func (m *Manifest) linkErrors() []string {
	var out []string
	for _, svc := range m.Services {
		for _, name := range svc.Uses {
			switch {
			case name == svc.Name:
				out = append(out, fmt.Sprintf("services %s: uses itself", svc.Name))
			default:
				provider, ok := m.service(name)
				switch {
				case !ok:
					out = append(out, fmt.Sprintf("services %s: uses %s, which urbex.yaml does not declare", svc.Name, name))
				case provider.Provides == "" && (provider.Healthcheck == "" || provider.Port == 0):
					// The wait needs to tell when the provider can take
					// calls: a port that answers doesn't say it.
					out = append(out, fmt.Sprintf("services %s: uses %s, which has neither an interface nor a healthcheck: give %s a port and a healthcheck, so that %s starts once %s is ready", svc.Name, name, name, svc.Name, name))
				}
			}
		}
		for env, vars := range svc.Env {
			for key, value := range vars {
				for _, ref := range References(value) {
					out = append(out, m.referenceError(svc, env, key, ref)...)
				}
			}
		}
	}
	if len(out) == 0 {
		if cycle := m.cycle(); cycle != nil {
			out = append(out, "services: uses form a cycle ("+strings.Join(cycle, " -> ")+")")
		}
	}
	return out
}

func (m *Manifest) referenceError(svc Service, env, key string, ref Reference) []string {
	where := fmt.Sprintf("services %s: env.%s.%s references ${%s.%s}", svc.Name, env, key, ref.App, ref.Key)
	used := false
	for _, name := range svc.Uses {
		used = used || name == ref.App
	}
	if !used {
		return []string{where + ", but " + svc.Name + " does not use " + ref.App}
	}
	provider, ok := m.service(ref.App)
	if !ok {
		return nil // reported as an unknown application
	}
	for _, k := range InterfaceKeys(provider.Interface()) {
		if k == ref.Key {
			return nil
		}
	}
	return []string{where + fmt.Sprintf(", which %s does not export (it exports %s)", ref.App, strings.Join(InterfaceKeys(provider.Interface()), ", "))}
}

// cycle returns the first cycle of `uses` as a path that starts and ends
// at the same application, or nil.
func (m *Manifest) cycle() []string {
	const (
		unseen = iota
		walking
		done
	)
	state := map[string]int{}
	var path []string
	var visit func(name string) []string
	visit = func(name string) []string {
		switch state[name] {
		case done:
			return nil
		case walking:
			for i, n := range path {
				if n == name {
					return append(append([]string{}, path[i:]...), name)
				}
			}
		}
		state[name] = walking
		path = append(path, name)
		if svc, ok := m.service(name); ok {
			for _, dep := range svc.Uses {
				if c := visit(dep); c != nil {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		state[name] = done
		return nil
	}
	for _, svc := range m.Services {
		if c := visit(svc.Name); c != nil {
			return c
		}
	}
	return nil
}

// Depth is how many applications there are in the longest chain of
// `uses` upstream of name: 0 for one that uses nothing. It orders the
// LXCs' start after a reboot of the node (urbforge/urbex-cli#43). The
// manifest must have no cycle.
func (m *Manifest) Depth(name string) int {
	svc, ok := m.service(name)
	if !ok {
		return 0
	}
	depth := 0
	for _, dep := range svc.Uses {
		if d := 1 + m.Depth(dep); d > depth {
			depth = d
		}
	}
	return depth
}

// DefaultReadyWithin is how long an application may take to be ready when
// urbex.yaml doesn't say (ready_within), a PostgreSQL's first start
// (initdb) included.
const DefaultReadyWithin = "60s"

// ReadySeconds is how long the application may take to be ready after it
// starts: its ready_within, in seconds.
func (s Service) ReadySeconds() int {
	v := s.ReadyWithin
	if v == "" {
		v = DefaultReadyWithin
	}
	n, err := strconv.Atoi(v[:len(v)-1])
	if err != nil || n <= 0 {
		n, v = 60, "s" // the schema rejects anything else
	}
	if strings.HasSuffix(v, "m") {
		return n * 60
	}
	return n
}

// ReadyStableSeconds is how long a provider must answer without a single
// failure before the wait calls it ready: one in a crash loop that stays
// up less than this doesn't pass. One that stays up longer does - the
// probe can't see restarts; the reconciler does, and alerts
// (platform/komodo, urbforge/urbex-cli#43). Every level of the graph
// starts this much later.
const ReadyStableSeconds = 20

// WaitSeconds is how long the application named name waits for what it
// uses before giving up: the longest chain of ready_within, plus
// ReadyStableSeconds per level, upstream of it, since its wait starts
// with every other Stack's deploy. 0 for one that uses nothing. The
// manifest must have no cycle.
func (m *Manifest) WaitSeconds(name string) int {
	svc, ok := m.service(name)
	if !ok {
		return 0
	}
	longest := 0
	for _, dep := range svc.Uses {
		provider, ok := m.service(dep)
		if !ok {
			continue
		}
		if w := provider.ReadySeconds() + ReadyStableSeconds + m.WaitSeconds(dep); w > longest {
			longest = w
		}
	}
	return longest
}
