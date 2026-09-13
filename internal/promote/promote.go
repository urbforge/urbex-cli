// Package promote implements `urbex promote`'s Git side: creating and
// pushing the tag that, per docs/decisions/0010-promotion-flow.md in
// urbforge/urbex, is the actual signal a production release happened -
// "no deploy silently outside of Git".
package promote

import (
	"context"
	"fmt"
	"io"

	"github.com/urbforge/urbex-cli/internal/gittag"
	"github.com/urbforge/urbex-cli/internal/runner"
)

// Options controls a single Tag run.
type Options struct {
	RepoDir string // path to the app's git repo
	Remote  string // defaults to "origin"
	Version string // explicit vX.Y.Z, or "" to auto-increment the patch version
}

// Orchestrator creates and pushes the promotion tag.
type Orchestrator struct {
	Runner runner.CommandRunner
	Out    io.Writer
}

// Tag computes (or validates, if Options.Version is set) the release
// version, creates an annotated tag at HEAD, and pushes it. It returns
// the tag that was created.
func (o *Orchestrator) Tag(ctx context.Context, opts Options) (string, error) {
	version := opts.Version
	if version == "" {
		existing, err := gittag.ListTags(opts.RepoDir, "v*")
		if err != nil {
			return "", fmt.Errorf("listing existing tags: %w", err)
		}
		version = gittag.NextTag(existing)
	} else if _, ok := gittag.Parse(version); !ok {
		return "", fmt.Errorf("invalid version %q: expected vX.Y.Z", version)
	}

	remote := opts.Remote
	if remote == "" {
		remote = "origin"
	}

	if err := o.Runner.Run(ctx, runner.Command{
		Dir:  opts.RepoDir,
		Name: "git",
		Args: []string{"tag", "-a", version, "-m", "urbex promote " + version},
	}); err != nil {
		return "", fmt.Errorf("git tag: %w", err)
	}

	if err := o.Runner.Run(ctx, runner.Command{
		Dir:  opts.RepoDir,
		Name: "git",
		Args: []string{"push", remote, version},
	}); err != nil {
		return "", fmt.Errorf("git push %s %s: %w", remote, version, err)
	}

	return version, nil
}
