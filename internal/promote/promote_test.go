package promote

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/urbforge/urbex-cli/internal/runner"
)

type fakeRunner struct {
	calls []runner.Command
	err   error
}

func (f *fakeRunner) Run(ctx context.Context, c runner.Command) error {
	f.calls = append(f.calls, c)
	return f.err
}

func TestTag_ExplicitVersion(t *testing.T) {
	r := &fakeRunner{}
	o := &Orchestrator{Runner: r, Out: &bytes.Buffer{}}

	got, err := o.Tag(context.Background(), Options{RepoDir: ".", Version: "v2.0.0"})
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if got != "v2.0.0" {
		t.Errorf("Tag() = %q, want v2.0.0", got)
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected 2 commands (git tag, git push), got %d: %+v", len(r.calls), r.calls)
	}
	if r.calls[0].Name != "git" || r.calls[0].Args[0] != "tag" {
		t.Errorf("unexpected first command: %+v", r.calls[0])
	}
	if r.calls[1].Name != "git" || r.calls[1].Args[0] != "push" || r.calls[1].Args[1] != "origin" || r.calls[1].Args[2] != "v2.0.0" {
		t.Errorf("unexpected second command: %+v", r.calls[1])
	}
}

func TestTag_InvalidExplicitVersion(t *testing.T) {
	r := &fakeRunner{}
	o := &Orchestrator{Runner: r, Out: &bytes.Buffer{}}

	_, err := o.Tag(context.Background(), Options{RepoDir: ".", Version: "not-a-version"})
	if err == nil {
		t.Fatal("expected an error for an invalid version")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected no commands run for an invalid version, got: %+v", r.calls)
	}
}

func TestTag_CustomRemote(t *testing.T) {
	r := &fakeRunner{}
	o := &Orchestrator{Runner: r, Out: &bytes.Buffer{}}

	if _, err := o.Tag(context.Background(), Options{RepoDir: ".", Version: "v1.0.0", Remote: "upstream"}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if r.calls[1].Args[1] != "upstream" {
		t.Errorf("expected push to upstream, got: %+v", r.calls[1])
	}
}

func TestTag_PushFailureStopsAfterTagCommand(t *testing.T) {
	r := &fakeRunner{err: errors.New("network unreachable")}
	o := &Orchestrator{Runner: r, Out: &bytes.Buffer{}}

	_, err := o.Tag(context.Background(), Options{RepoDir: ".", Version: "v1.0.0"})
	if err == nil {
		t.Fatal("expected an error when the runner fails")
	}
	// Both git tag and git push are attempted (the fake fails on the
	// first call, "git tag"); Tag should stop there rather than push.
	if len(r.calls) != 1 {
		t.Errorf("expected exactly 1 command attempted before failing, got %d: %+v", len(r.calls), r.calls)
	}
}
