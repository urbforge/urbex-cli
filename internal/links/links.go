// Package links works out what applications of a stack pass to each
// other (ADR-0028, Stacks: how applications find each other, and
// ADR-0030): the settings of a provider, the connection variables of the
// applications that use it, and the `${app.key}` references of their
// env. It only computes; envrepo writes the files and the orchestrator
// keeps the secrets.
package links

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/urbforge/urbex-cli/internal/manifest"
)

const (
	// PostgresPort is the port a postgres provider listens on (ADR-0030).
	PostgresPort = 5432
	// PostgresAdmin is the server's own user, used only by urbex.
	PostgresAdmin = "urbex"
)

// Conn is a provider as one of the applications that use it sees it.
type Conn struct {
	Name      string // the provider application
	Interface string
	Host      string // its internal name
	Port      int
	// User, Password and Database are the application's own role on a
	// postgres provider.
	User, Password, Database string
}

// Value is the value a reference `${name.key}` takes. ok is false for a
// key the connection doesn't export.
func (c Conn) Value(key string) (string, bool) {
	switch key {
	case "host":
		return c.Host, true
	case "port":
		return strconv.Itoa(c.Port), true
	case "url":
		return c.URL(), true
	}
	if c.Interface == manifest.InterfacePostgres {
		switch key {
		case "user":
			return c.User, true
		case "password":
			return c.Password, true
		case "database":
			return c.Database, true
		}
	}
	return "", false
}

// URL is the connection URL of the provider.
func (c Conn) URL() string {
	if c.Interface == manifest.InterfacePostgres {
		return fmt.Sprintf("postgres://%s:%s@%s:%d/%s", c.User, c.Password, c.Host, c.Port, c.Database)
	}
	return fmt.Sprintf("http://%s:%d", c.Host, c.Port)
}

// secret reports whether the value of key holds a password.
func (c Conn) secret(key string) bool {
	return c.Interface == manifest.InterfacePostgres && (key == "password" || key == "url")
}

// Variables are the connection variables an application gets from the
// providers it uses, plain and secret apart. An application that uses
// more than one provider of an interface gets that interface's
// variables prefixed with the provider's name.
func Variables(conns []Conn) (plain, secret map[string]string) {
	plain, secret = map[string]string{}, map[string]string{}
	perInterface := map[string]int{}
	for _, c := range conns {
		perInterface[c.Interface]++
	}
	for _, c := range conns {
		switch c.Interface {
		case manifest.InterfacePostgres:
			prefix := ""
			if perInterface[c.Interface] > 1 {
				prefix = envName(c.Name) + "_"
			}
			secret[prefix+"DATABASE_URL"] = c.URL()
			secret[prefix+"PGPASSWORD"] = c.Password
			plain[prefix+"PGHOST"] = c.Host
			plain[prefix+"PGPORT"] = strconv.Itoa(c.Port)
			plain[prefix+"PGDATABASE"] = c.Database
			plain[prefix+"PGUSER"] = c.User
		default:
			app := envName(c.Name)
			plain[app+"_HOST"] = c.Host
			plain[app+"_PORT"] = strconv.Itoa(c.Port)
			plain[app+"_URL"] = c.URL()
		}
	}
	return plain, secret
}

// envName is an application's name as a variable prefix: capitals, `-`
// turned into `_`.
func envName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// RoleName is the PostgreSQL role (and schema) of an application:
// its name with `-` turned into `_` (ADR-0030).
func RoleName(app string) string { return strings.ReplaceAll(app, "-", "_") }

// DatabaseName is the database of a project's provider (ADR-0030).
func DatabaseName(project string) string { return strings.ReplaceAll(project, "-", "_") }

// Resolve replaces the `${app.key}` references in value with what the
// connections export. secret reports whether the result holds a password,
// and so can only be kept encrypted. An unknown application or key is an
// error.
func Resolve(value string, conns map[string]Conn) (resolved string, secret bool, err error) {
	resolved = value
	for _, ref := range manifest.References(value) {
		c, ok := conns[ref.App]
		if !ok {
			return "", false, fmt.Errorf("${%s.%s}: %s is not used", ref.App, ref.Key, ref.App)
		}
		v, ok := c.Value(ref.Key)
		if !ok {
			return "", false, fmt.Errorf("${%s.%s}: %s does not export %s", ref.App, ref.Key, ref.App, ref.Key)
		}
		secret = secret || c.secret(ref.Key)
		resolved = strings.ReplaceAll(resolved, "${"+ref.App+"."+ref.Key+"}", v)
	}
	return resolved, secret, nil
}

// Provider is what urbex configures a postgres provider with: the
// server's user and database, its data directory inside the volume, and
// its generated password (ADR-0030).
func Provider(project string, svc manifest.Service, password string) (plain, secret map[string]string) {
	plain = map[string]string{
		"POSTGRES_USER": PostgresAdmin,
		"POSTGRES_DB":   DatabaseName(project),
	}
	if volumes := svc.NamedVolumes(); len(volumes) == 1 {
		plain["PGDATA"] = strings.TrimSuffix(volumes[0].Path, "/") + "/pgdata"
	}
	return plain, map[string]string{"POSTGRES_PASSWORD": password}
}

// PasswordKey is the secret under which an application keeps its role's
// password on a provider, whatever prefix its variables get.
func PasswordKey(provider string) string { return "URBEX_PASSWORD_" + envName(provider) }

// Plan is what the generated files of a stack's applications hold in an
// environment, by application.
type Plan struct {
	Plain, Secret map[string]map[string]string
}

// Inputs are what Build needs from outside.
type Inputs struct {
	// Host is an application's internal name in the environment.
	Host func(svc manifest.Service) string
	// Existing are the secrets each application's generated file already
	// keeps, so that a password is generated once and reused.
	Existing map[string]map[string]string
	// Password makes a new password.
	Password func() string
}

// Build works out the generated variables of every application of the
// stack that provides or uses something, in env.
func Build(m *manifest.Manifest, env string, in Inputs) (Plan, error) {
	plan := Plan{Plain: map[string]map[string]string{}, Secret: map[string]map[string]string{}}
	add := func(app string, plain, secret map[string]string) {
		if plan.Plain[app] == nil {
			plan.Plain[app], plan.Secret[app] = map[string]string{}, map[string]string{}
		}
		for k, v := range plain {
			plan.Plain[app][k] = v
		}
		for k, v := range secret {
			plan.Secret[app][k] = v
		}
	}
	keep := func(app, key string) string {
		if v := in.Existing[app][key]; v != "" {
			return v
		}
		return in.Password()
	}
	adminPassword := map[string]string{}
	for _, svc := range m.Services {
		if svc.Provides == manifest.InterfacePostgres {
			adminPassword[svc.Name] = keep(svc.Name, "POSTGRES_PASSWORD")
			plain, secret := Provider(m.Project, svc, adminPassword[svc.Name])
			add(svc.Name, plain, secret)
		}
	}
	for _, svc := range m.Services {
		if len(svc.Uses) == 0 {
			continue
		}
		var conns []Conn
		byName := map[string]Conn{}
		secret := map[string]string{}
		for _, name := range svc.Uses {
			provider, _ := m.Service(name)
			c := Conn{Name: name, Interface: provider.Provides, Host: in.Host(provider), Port: provider.Port}
			if provider.Provides == manifest.InterfacePostgres {
				c.Port = PostgresPort
				c.User, c.Database = RoleName(svc.Name), DatabaseName(m.Project)
				c.Password = keep(svc.Name, PasswordKey(name))
				secret[PasswordKey(name)] = c.Password
			}
			conns = append(conns, c)
			byName[name] = c
		}
		plain, secrets := Variables(conns)
		for k, v := range secrets {
			secret[k] = v
		}
		keys := make([]string, 0, len(svc.EnvFor(env)))
		for k := range svc.EnvFor(env) {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := svc.EnvFor(env)[k]
			if len(manifest.References(v)) == 0 {
				continue
			}
			resolved, isSecret, err := Resolve(v, byName)
			if err != nil {
				return Plan{}, fmt.Errorf("services %s: env.%s.%s: %w", svc.Name, env, k, err)
			}
			if isSecret {
				secret[k] = resolved
			} else {
				plain[k] = resolved
			}
		}
		add(svc.Name, plain, secret)
	}
	return plan, nil
}

// NewPassword is 32 random letters and digits, with nothing to escape in
// a URL (ADR-0030).
func NewPassword() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	out := make([]byte, 32)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err) // crypto/rand does not fail on supported systems
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out)
}

// Keys are the variables urbex generates for an application in a plan,
// plain and secret: the ones `urbex secret` refuses to set.
func (p Plan) Keys(app string) []string {
	var keys []string
	for k := range p.Plain[app] {
		keys = append(keys, k)
	}
	for k := range p.Secret[app] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
