package runner

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type flaky struct {
	fails int
	calls []Command
}

func (f *flaky) Run(_ context.Context, c Command) error {
	f.calls = append(f.calls, c)
	if len(f.calls) <= f.fails {
		return errors.New("exit status 1")
	}
	return nil
}

func TestTerraformChange_RetriesOnceWithLimitedParallelism(t *testing.T) {
	r := &flaky{fails: 1}
	var out bytes.Buffer
	if err := TerraformChange(context.Background(), r, &out, "/tf", "apply", nil); err != nil {
		t.Fatalf("expected the retry to succeed: %v", err)
	}
	if len(r.calls) != 2 || strings.Join(r.calls[0].Args, " ") != "apply -auto-approve -parallelism=2" {
		t.Errorf("unexpected calls %+v", r.calls)
	}
	if !strings.Contains(out.String(), "trying once more") {
		t.Errorf("the retry should be announced: %q", out.String())
	}

	r = &flaky{fails: 2}
	if err := TerraformChange(context.Background(), r, &out, "/tf", "destroy", nil); err == nil || len(r.calls) != 2 {
		t.Errorf("expected to give up after two tries, got %v after %d calls", err, len(r.calls))
	}
}
