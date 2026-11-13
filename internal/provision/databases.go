package provision

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/urbforge/urbex-cli/internal/imageref"
	"github.com/urbforge/urbex-cli/internal/links"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/pgadmin"
)

// postgresProviders are the environment's postgres providers.
func (o *Orchestrator) postgresProviders() []manifest.Service {
	var out []manifest.Service
	for _, svc := range o.deployables() {
		if svc.Provides == manifest.InterfacePostgres {
			out = append(out, svc)
		}
	}
	return out
}

// consumers are the applications that use provider.
func (o *Orchestrator) consumers(provider string) []manifest.Service {
	var out []manifest.Service
	for _, svc := range o.Manifest.Services {
		for _, name := range svc.Uses {
			if name == provider {
				out = append(out, svc)
			}
		}
	}
	return out
}

// serverReadyPoll is how often ensureDatabases asks a provider whether
// it takes connections yet.
var serverReadyPoll = 3 * time.Second

// ensureDatabases makes sure every application that uses a postgres
// provider has its role, schema and password on it (ADR-0030), once the
// provider takes connections - which the applications' waits also wait
// for, logging in with their own role. It lists the spaces of the
// applications that use it no more, and drops them with deleteData. It
// runs once per command: needs linkedTarget's plan.
func (o *Orchestrator) ensureDatabases(ctx context.Context, env string, deleteData bool) error {
	if o.databasesDone || o.linkPlan.Secret == nil {
		// Done, or no command here that generates a password.
		return nil
	}
	for _, provider := range o.postgresProviders() {
		if err := o.ensureDatabase(ctx, env, provider, deleteData); err != nil {
			return fmt.Errorf("%s: %w", provider.Name, err)
		}
	}
	o.databasesDone = true
	return nil
}

func (o *Orchestrator) ensureDatabase(ctx context.Context, env string, provider manifest.Service, deleteData bool) error {
	shell := o.Rollout.Shell(o.Manifest.Project, env, provider.Name)
	defer shell.Close(context.WithoutCancel(ctx))
	server := pgadmin.Server{Shell: shell}
	if err := o.waitServer(ctx, server, provider); err != nil {
		return err
	}
	var roles []pgadmin.Role
	used := map[string]bool{}
	for _, svc := range o.consumers(provider.Name) {
		password := o.linkPlan.Secret[svc.Name][links.PasswordKey(provider.Name)]
		if password == "" {
			return fmt.Errorf("no password for %s's role: the links weren't worked out", svc.Name)
		}
		roles = append(roles, pgadmin.Role{Name: links.RoleName(svc.Name), Password: password})
		used[links.RoleName(svc.Name)] = true
	}
	changed, err := server.EnsureRoles(ctx, roles)
	if err != nil {
		return fmt.Errorf("creating the applications' roles and schemas: %w", err)
	}
	for _, c := range changed {
		fmt.Fprintf(o.Out, "%s: %s.\n", provider.Name, c)
	}
	spaces, err := server.Spaces(ctx)
	if err != nil {
		return fmt.Errorf("listing the roles and schemas: %w", err)
	}
	unused := pgadmin.Unused(spaces, used)
	if len(unused) == 0 {
		return nil
	}
	if !deleteData {
		fmt.Fprintf(o.Out, "%s: no application uses the schema and role %s any more: kept, with their data - 'urbex apply %s --delete-data' drops them.\n",
			provider.Name, strings.Join(unused, ", "), env)
		return nil
	}
	fmt.Fprintf(o.Out, "%s: dropping the schema and role %s, which no application uses, with their data (--delete-data).\n", provider.Name, strings.Join(unused, ", "))
	if err := server.Drop(ctx, unused); err != nil {
		return fmt.Errorf("dropping %s: %w", strings.Join(unused, ", "), err)
	}
	return nil
}

// waitServer waits for the provider to take connections: as long as its
// first start (initdb) may take, and a minute for its deploy.
func (o *Orchestrator) waitServer(ctx context.Context, server pgadmin.Server, provider manifest.Service) error {
	budget := time.Duration(provider.ReadySeconds())*time.Second + time.Minute
	deadline := time.Now().Add(budget)
	var last error
	for {
		ready, err := server.Ready(ctx)
		if ready {
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			if last != nil {
				return fmt.Errorf("it doesn't take connections: %w", last)
			}
			return fmt.Errorf("it doesn't take connections after %s", budget)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(serverReadyPoll):
		}
	}
}

// deployDatabases deploys the postgres providers, then their
// applications' roles and schemas: before the applications, whose
// waits log in with them.
func (o *Orchestrator) deployDatabases(ctx context.Context, env string, deleteData bool) error {
	providers := o.postgresProviders()
	if len(providers) == 0 {
		return nil
	}
	if err := o.Rollout.Deploy(ctx, o.targetOf(env, providers)); err != nil {
		return err
	}
	return o.ensureDatabases(ctx, env, deleteData)
}

// checkDataVersion refuses to run a postgres provider at a major version
// other than its data directory's (PG_VERSION): a new major can't open
// the old one, --allow-major or not (ADR-0030). An empty data directory
// takes any. So does a provider whose data directory can't be read -
// stopped for the manual upgrade, whose data directory is moved aside
// meanwhile - with a warning: PostgreSQL itself refuses to start on
// another major's data, and leaves it as it is.
func (o *Orchestrator) checkDataVersion(ctx context.Context, env string, svc manifest.Service, to string) error {
	if svc.Provides != manifest.InterfacePostgres {
		return nil
	}
	want, ok := imageref.Major(to)
	if !ok {
		return nil
	}
	shell := o.Rollout.Shell(o.Manifest.Project, env, svc.Name)
	defer shell.Close(context.WithoutCancel(ctx))
	have, err := pgadmin.Server{Shell: shell}.DataVersion(ctx)
	if err != nil {
		fmt.Fprintf(o.Out, "%s: can't read its data directory's major version (%v) - it isn't running? Deploying %s: on a data directory of another major it won't start, and 'urbex deploy %s --app %s --version <the old tag>' brings the old one back.\n",
			svc.Name, err, to, env, svc.Name)
		return nil
	}
	if have == "" || have == fmt.Sprint(want) {
		return nil
	}
	return fmt.Errorf("%s's data directory is PostgreSQL %s, which %s can't open: dump it with pg_dumpall, empty its volume, deploy %s with --allow-major and restore the dump - see the cookbook (a major upgrade of PostgreSQL by hand)", svc.Name, have, to, to)
}
