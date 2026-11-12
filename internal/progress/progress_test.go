package progress

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var specs = []PhaseSpec{
	{ID: "template", Title: "Build the LXC template", Estimate: 200 * time.Second},
	{ID: "lxcs", Title: "Create the LXCs", Estimate: 50 * time.Second},
	{ID: "common", Title: "Configure every LXC", Estimate: 25 * time.Second, Play: "Configure all urbex base-service containers"},
	{ID: "stacks", Title: "Deploy the Stacks", Estimate: 90 * time.Second},
}

// clock is a fake time that only moves when told; Watch reads it from
// its own goroutine.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
func newClock() *clock { return &clock{t: time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)} }
func startAt(t *testing.T, dir string, c *clock) *Tracker {
	t.Helper()
	tr, err := start(dir, "bootstrap", "urbex bootstrap", specs, c.now, false, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestTracker_RecordsPhasesAndEstimatesTheRest(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	tr := startAt(t, dir, c)
	tr.Skip("template", "the current one is there")
	tr.Begin("lxcs")
	c.add(40 * time.Second)
	tr.Begin("common")
	c.add(10 * time.Second)

	r, err := Latest(dir)
	if err != nil || r == nil {
		t.Fatalf("Latest = %v, %v", r, err)
	}
	got := map[string]State{}
	for _, p := range r.Phases {
		got[p.ID] = p.State
	}
	if got["template"] != Skipped || got["lxcs"] != Done || got["common"] != Running || got["stacks"] != Pending {
		t.Errorf("states = %v", got)
	}
	// 15 s left of common's 25, and stacks' 90.
	if left := r.Remaining(c.now()); left != 105*time.Second {
		t.Errorf("Remaining = %v, want 1m45s", left)
	}
	var out bytes.Buffer
	r.Print(&out, c.now())
	for _, want := range []string{
		"urbex bootstrap: running for 50s, about 1m45s left",
		"- Build the LXC template  skipped: the current one is there",
		"✓ Create the LXCs         40s",
		"▶ Configure every LXC     10s so far, about 15s left",
		"· Deploy the Stacks       about 1m30s",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Print lacks %q:\n%s", want, out.String())
		}
	}
}

// The next run's estimates are what the phases took last time.
func TestTracker_EstimatesFromTheLastSuccessfulRuns(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	for _, took := range []time.Duration{30 * time.Second, 50 * time.Second} {
		tr := startAt(t, dir, c)
		tr.Begin("lxcs")
		c.add(took)
		tr.Finish(nil)
		c.add(time.Hour)
	}
	// A failed run doesn't count.
	tr := startAt(t, dir, c)
	tr.Begin("lxcs")
	c.add(10 * time.Minute)
	tr.Finish(errors.New("boom"))
	c.add(time.Hour)

	tr = startAt(t, dir, c)
	est := map[string]time.Duration{}
	for _, p := range tr.run.Phases {
		est[p.ID] = time.Duration(p.Estimate)
	}
	if est["lxcs"] != 40*time.Second {
		t.Errorf("lxcs estimate = %v, want the average of 30s and 50s", est["lxcs"])
	}
	if est["stacks"] != 90*time.Second {
		t.Errorf("stacks estimate = %v, want its default: no run did it", est["stacks"])
	}
}

func TestTracker_AFailedRunShowsTheFailedPhase(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	tr := startAt(t, dir, c)
	tr.Begin("lxcs")
	c.add(20 * time.Second)
	tr.Finish(errors.New("terraform apply: exit status 1"))
	r, _ := Latest(dir)
	var out bytes.Buffer
	r.Print(&out, c.now())
	for _, want := range []string{"urbex bootstrap: failed after 20s", "✗ Create the LXCs", "failed after 20s", "Error: terraform apply: exit status 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Print lacks %q:\n%s", want, out.String())
		}
	}
	if r.Phases[3].State != Pending {
		t.Errorf("phases after the failure stay pending, got %s", r.Phases[3].State)
	}
}

// A run that stops rewriting its record - killed, crashed - isn't shown
// as running forever.
func TestRun_StaleWhenTheCommandIsGone(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	tr := startAt(t, dir, c)
	tr.Begin("lxcs")
	r, _ := Latest(dir)
	if r.Stale(c.now().Add(time.Minute)) {
		t.Error("a minute without news is still running")
	}
	c.add(5 * time.Minute)
	if !r.Stale(c.now()) {
		t.Fatal("five minutes without news: the command is gone")
	}
	var out bytes.Buffer
	r.Print(&out, c.now())
	if !strings.Contains(out.String(), "stopped without finishing") {
		t.Errorf("Print:\n%s", out.String())
	}
}

// Ansible's play headers begin their phases.
func TestTracker_WatchBeginsPhasesFromPlayHeaders(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	tr := startAt(t, dir, c)
	tr.Begin("lxcs")
	var out bytes.Buffer
	w := tr.Watch(&out)
	fmt.Fprintln(w, "PLAY [Configure all urbex base-service containers] *****")
	fmt.Fprintln(w, "TASK [common : Install Docker] ****")
	deadline := time.Now().Add(2 * time.Second)
	for {
		r, _ := Latest(dir)
		if r.Phases[2].State == Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the play didn't begin its phase: %+v", r.Phases)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "TASK [common : Install Docker]") {
		t.Error("Watch must pass the output on")
	}
}

func TestTracker_KeepsTheLastRuns(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	for i := 0; i < history+3; i++ {
		startAt(t, dir, c).Finish(nil)
		c.add(time.Minute)
	}
	if n := len(files(dir, "bootstrap")); n != history {
		t.Errorf("%d runs kept, want %d", n, history)
	}
}

// Latest is the newest run of any kind.
func TestLatest_AcrossKinds(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	startAt(t, dir, c).Finish(nil)
	c.add(time.Minute)
	tr, _ := start(dir, "apply", "urbex apply staging", specs, c.now, false, Options{})
	tr.Finish(nil)
	if r, _ := Latest(dir); r.Command != "urbex apply staging" {
		t.Errorf("Latest = %s, want the apply that came after", r.Command)
	}
}

func TestNilTrackerRecordsNothing(t *testing.T) {
	var tr *Tracker
	tr.Begin("lxcs")
	tr.Skip("template", "")
	tr.Finish(nil)
	var out bytes.Buffer
	if w := tr.Watch(&out); w != &out {
		t.Error("a nil Tracker's Watch is the writer itself")
	}
}
