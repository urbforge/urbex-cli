package provision

import (
	"context"
	"fmt"
	"reflect"

	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/links"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/rollout"
)

// linkedTarget is the target of env with what the applications give each
// other (ADR-0028): the generated variables of each application that
// provides or uses something, and what its Stack waits for. The values
// that hold a password are encrypted into the application's generated
// file here, and kept: a password is generated once.
func (o *Orchestrator) linkedTarget(ctx context.Context, env string, repo envrepo.Repo) (rollout.Target, error) {
	t := o.target(env)
	if !o.hasLinks() {
		return t, nil
	}
	store, err := o.secretStore()
	if err != nil {
		return t, fmt.Errorf("the links between applications keep passwords encrypted: %w", err)
	}
	existing := map[string]map[string]string{}
	for _, svc := range o.Manifest.Services {
		kept, err := store.Read(ctx, repo.GeneratedSecretsPath(env, o.Manifest.Project, svc.Name))
		if err != nil {
			return t, err
		}
		existing[svc.Name] = kept
	}
	plan, err := links.Build(o.Manifest, env, links.Inputs{
		Host:     func(svc manifest.Service) string { return o.InternalHostname(env, svc) },
		Existing: existing,
		Password: links.NewPassword,
	})
	if err != nil {
		return t, err
	}
	o.linkPlan = plan
	t.Links = map[string]*envrepo.Links{}
	for _, svc := range o.Manifest.Services {
		if _, ok := plan.Plain[svc.Name]; !ok {
			continue
		}
		l := &envrepo.Links{Env: plan.Plain[svc.Name], WaitSeconds: o.Manifest.WaitSeconds(svc.Name)}
		for _, name := range svc.Uses {
			provider, _ := o.Manifest.Service(name)
			w := envrepo.Wait{Name: name, Interface: provider.Provides, Host: o.InternalHostname(env, provider), Port: provider.Port, Healthcheck: provider.Healthcheck}
			if provider.Provides == manifest.InterfacePostgres {
				// Its wait logs in with the application's own role: the
				// role exists, and has this password (ADR-0030).
				w.Port = links.PostgresPort
				w.User, w.Database = links.RoleName(svc.Name), links.DatabaseName(o.Manifest.Project)
				w.PasswordVar = links.PasswordKey(name)
			}
			l.Waits = append(l.Waits, w)
		}
		t.Links[svc.Name] = l
		// Encrypting gives a different file every time: write only when
		// the values changed, so that apply does not rewrite the repo.
		if reflect.DeepEqual(existing[svc.Name], plan.Secret[svc.Name]) {
			continue
		}
		path := repo.GeneratedSecretsPath(env, o.Manifest.Project, svc.Name)
		if err := store.Write(ctx, path, plan.Secret[svc.Name]); err != nil {
			return t, err
		}
	}
	return t, nil
}

// hasLinks reports whether any application provides or uses another.
func (o *Orchestrator) hasLinks() bool {
	for _, svc := range o.Manifest.Services {
		if svc.Provides != "" || len(svc.Uses) > 0 {
			return true
		}
	}
	return false
}
