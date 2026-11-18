package provision

import (
	"context"
	"fmt"

	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/imageref"
	"github.com/urbforge/urbex-cli/internal/manifest"
)

// TagLister lists a published image's tags - see internal/imageref.
type TagLister interface {
	Tags(ctx context.Context, image string) ([]string, error)
}

// ImageUpdate is what's newer for an application from a published image
// in an environment (urbforge/urbex-cli#113).
type ImageUpdate struct {
	Env   string `json:"env"`
	App   string `json:"app"`
	Image string `json:"image"`
	// Asked is the tag urbex.yaml asks for; Running what the environment
	// runs (tag@digest).
	Asked   string `json:"asked"`
	Running string `json:"running"`
	// NewDigest: the asked tag points to another image than the one
	// running - 'urbex deploy <env> --update' moves to it.
	NewDigest bool `json:"newDigest,omitempty"`
	// Newer tags of the same shape as the asked one: Minor of the same
	// major (deploy it with --app and --version), Major of a higher one -
	// listed, never proposed: its data may need a migration.
	imageref.Updates
	// Error is why it couldn't be checked, if it couldn't.
	Error string `json:"error,omitempty"`
}

// Updates checks, for each application from a published image applied in
// env (or in every environment, env ""), whether a newer image is
// available: the asked tag resolved again, and the image's newer tags.
// It changes nothing.
func (o *Orchestrator) Updates(ctx context.Context, opts Options) ([]ImageUpdate, error) {
	envs := []string{"staging", "prod"}
	if opts.Env != "" {
		if !ValidEnvs[opts.Env] {
			return nil, fmt.Errorf("invalid environment %q: must be one of staging, prod", opts.Env)
		}
		envs = []string{opts.Env}
	}
	apps := manifest.FromImages(o.Manifest.Deployables())
	if len(apps) == 0 {
		return nil, fmt.Errorf("%s has no application from a published image: nothing to check", o.Manifest.Project)
	}
	if o.Images == nil {
		return nil, fmt.Errorf("no image resolver")
	}
	lister, _ := o.Images.(TagLister)
	if err := o.refresh(ctx, opts); err != nil {
		return nil, err
	}
	repo := envrepo.Repo{Path: opts.GitOpsRepoPath}
	tags := map[string][]string{}
	tagErr := map[string]error{}
	digests := map[string]string{}
	var out []ImageUpdate
	for _, env := range envs {
		for _, svc := range apps {
			st, err := repo.Version(env, o.Manifest.Project, svc.Name)
			if err != nil {
				return nil, err
			}
			running, ok := imageref.ParsePinned(st.Version)
			if !ok {
				continue // not applied in env, or not pinned yet
			}
			u := ImageUpdate{Env: env, App: svc.Name, Image: svc.Image, Asked: svc.Version, Running: running.String()}
			key := svc.Image + ":" + svc.Version
			digest, seen := digests[key]
			if !seen {
				if digest, err = o.Images.Resolve(ctx, svc.Image, svc.Version); err != nil {
					u.Error = err.Error()
				}
				digests[key] = digest
			}
			u.NewDigest = digest != "" && running.Tag == svc.Version && digest != running.Digest
			if lister != nil {
				if _, seen := tags[svc.Image]; !seen && tagErr[svc.Image] == nil {
					tags[svc.Image], tagErr[svc.Image] = lister.Tags(ctx, svc.Image)
				}
				if err := tagErr[svc.Image]; err != nil && u.Error == "" {
					u.Error = err.Error()
				}
				u.Updates = imageref.Newer(svc.Version, tags[svc.Image])
			}
			out = append(out, u)
		}
	}
	return out, nil
}
