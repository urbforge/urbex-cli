// Package progress records how a long command - urbex bootstrap, urbex
// apply - is getting on: its phases, the one running, how long each took
// and how long the rest should take. The command writes the record to a
// file as it goes; `urbex status --progress` reads it from another
// terminal (urbforge/urbex-cli#37).
package progress

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// PhaseSpec is a phase a command goes through.
type PhaseSpec struct {
	ID    string
	Title string
	// Estimate is the phase's usual duration when no earlier run says
	// better: measured on the test Proxmox node.
	Estimate time.Duration
	// Play, if set, is the name of the Ansible play that starts this
	// phase: Watch begins the phase when the play's header goes by.
	Play string
}

// State is where a phase is.
type State string

const (
	Pending State = "pending"
	Running State = "running"
	Done    State = "done"
	Skipped State = "skipped"
	Failed  State = "failed"
)

// Phase is a phase of a run.
type Phase struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	State    State      `json:"state"`
	Started  *time.Time `json:"started,omitempty"`
	Ended    *time.Time `json:"ended,omitempty"`
	Estimate Seconds    `json:"estimate"`
	Note     string     `json:"note,omitempty"`
}

// Took is how long a finished phase ran.
func (p Phase) Took() time.Duration {
	if p.Started == nil || p.Ended == nil {
		return 0
	}
	return p.Ended.Sub(*p.Started)
}

// Run is one run of a command, as recorded.
type Run struct {
	Kind    string     `json:"kind"`    // bootstrap, apply
	Command string     `json:"command"` // e.g. "urbex apply staging"
	Started time.Time  `json:"started"`
	Updated time.Time  `json:"updated"` // the heartbeat
	Ended   *time.Time `json:"ended,omitempty"`
	Result  string     `json:"result"` // running, ok, failed
	Error   string     `json:"error,omitempty"`
	Phases  []Phase    `json:"phases"`
	// Log is the file the command's output goes to with --events json;
	// "" when it went to the terminal.
	Log string `json:"log,omitempty"`
}

// Results of a run.
const (
	ResultRunning = "running"
	ResultOK      = "ok"
	ResultFailed  = "failed"
)

// Heartbeat is how often a running command rewrites its record; a record
// not rewritten for StaleAfter belongs to a command that stopped without
// finishing (killed, crashed, a lost terminal).
const (
	Heartbeat  = 10 * time.Second
	StaleAfter = 2 * time.Minute
)

// Seconds is a duration written in whole seconds.
type Seconds time.Duration

func (s Seconds) MarshalJSON() ([]byte, error) {
	return json.Marshal(int64(time.Duration(s).Round(time.Second) / time.Second))
}

func (s *Seconds) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*s = Seconds(time.Duration(n) * time.Second)
	return nil
}

// Tracker records a run as it goes. A nil Tracker records nothing, so
// callers needn't check.
type Tracker struct {
	mu    sync.Mutex
	path  string
	run   Run
	plays map[string]string // play name -> phase ID
	now   func() time.Time
	stop  chan struct{}
	// live gets the events as they happen (--events json), besides the
	// run's events file; nil for none.
	live io.Writer
}

// history keeps this many runs of each kind: the newest give the
// estimates, all are there to look at.
const history = 10

// Start begins recording a run of a command of the given kind, in dir.
// The phases' estimates come from the same phases of the last successful
// runs of that kind in dir, or from their specs.
func Start(dir, kind, command string, specs []PhaseSpec) (*Tracker, error) {
	return start(dir, kind, command, specs, time.Now, true, Options{})
}

// Options are what a run records besides its phases.
type Options struct {
	// Log is where the command's output goes (Run.Log).
	Log string
	// Live gets the run's events as they happen, as JSON lines; write
	// errors are ignored, so a reader that goes away doesn't stop the run.
	Live io.Writer
}

// StartWith is Start with options.
func StartWith(dir, kind, command string, specs []PhaseSpec, opts Options) (*Tracker, error) {
	return start(dir, kind, command, specs, time.Now, true, opts)
}

func start(dir, kind, command string, specs []PhaseSpec, now func() time.Time, heartbeat bool, opts Options) (*Tracker, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	past := estimates(dir, kind)
	t := &Tracker{plays: map[string]string{}, now: now, live: opts.Live}
	started := now()
	t.run = Run{Kind: kind, Command: command, Started: started, Updated: started, Result: ResultRunning, Log: opts.Log}
	for _, s := range specs {
		est := s.Estimate
		if d, ok := past[s.ID]; ok {
			est = d
		}
		t.run.Phases = append(t.run.Phases, Phase{ID: s.ID, Title: s.Title, State: Pending, Estimate: Seconds(est)})
		if s.Play != "" {
			t.plays[s.Play] = s.ID
		}
	}
	t.path = filepath.Join(dir, fmt.Sprintf("%s-%s.json", kind, started.UTC().Format("20060102-150405.000")))
	if err := t.save(); err != nil {
		return nil, err
	}
	t.emit(Event{Type: EventRunStart, Log: opts.Log})
	prune(dir, kind)
	if heartbeat {
		t.stop = make(chan struct{})
		go t.beat()
	}
	return t, nil
}

func (t *Tracker) beat() {
	tick := time.NewTicker(Heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-tick.C:
			t.mu.Lock()
			t.save()
			t.mu.Unlock()
		}
	}
}

// Begin starts a phase; the one running, if any, is done.
func (t *Tracker) Begin(id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for i := range t.run.Phases {
		p := &t.run.Phases[i]
		switch {
		case p.State == Running && p.ID != id:
			p.State, p.Ended = Done, &now
			t.emit(Event{Type: EventPhaseEnd, Phase: p.ID, Title: p.Title, Seconds: secondsOf(p.Took())})
		case p.ID == id && p.State != Running:
			p.State, p.Started, p.Ended = Running, &now, nil
			est := p.Estimate
			t.emit(Event{Type: EventPhaseStart, Phase: p.ID, Title: p.Title, Estimate: &est})
		}
	}
	t.save()
}

// Skip marks a phase that won't run, saying why.
func (t *Tracker) Skip(id, why string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.run.Phases {
		if p := &t.run.Phases[i]; p.ID == id {
			p.State, p.Note = Skipped, why
			t.emit(Event{Type: EventPhaseSkip, Phase: p.ID, Title: p.Title, Note: why})
		}
	}
	t.save()
}

// Finish ends the run: with err nil, the running phase is done and those
// never begun were skipped; otherwise the running phase failed.
func (t *Tracker) Finish(err error) {
	if t == nil {
		return
	}
	if t.stop != nil {
		close(t.stop)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.run.Ended = &now
	t.run.Result = ResultOK
	if err != nil {
		t.run.Result, t.run.Error = ResultFailed, err.Error()
	}
	for i := range t.run.Phases {
		p := &t.run.Phases[i]
		switch {
		case p.State == Running && err != nil:
			p.State, p.Ended = Failed, &now
			t.emit(Event{Type: EventPhaseFail, Phase: p.ID, Title: p.Title, Seconds: secondsOf(p.Took()), Error: t.run.Error})
		case p.State == Running:
			p.State, p.Ended = Done, &now
			t.emit(Event{Type: EventPhaseEnd, Phase: p.ID, Title: p.Title, Seconds: secondsOf(p.Took())})
		case p.State == Pending && err == nil:
			p.State = Skipped
		}
	}
	t.save()
	t.emit(Event{Type: EventRunEnd, Result: t.run.Result, Error: t.run.Error, Seconds: secondsOf(now.Sub(t.run.Started))})
}

// save writes the record atomically: a reader never sees half of it.
// Called with t.mu held. A failed write only loses progress, never the
// command: it is ignored.
func (t *Tracker) save() error {
	t.run.Updated = t.now()
	data, err := json.MarshalIndent(t.run, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

var playHeader = regexp.MustCompile(`PLAY \[(.+?)\]`)

// Watch returns a writer that copies to w, begins the phase whose
// Ansible play starts in what goes through it, and records a step for
// each Ansible task and each machine Terraform finishes with.
func (t *Tracker) Watch(w io.Writer) io.Writer {
	if t == nil {
		return w
	}
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if m := playHeader.FindStringSubmatch(line); m != nil {
				if id, ok := t.plays[m[1]]; ok {
					t.Begin(id)
				}
			}
			if step := stepOf(line); step != "" {
				t.Step(step)
			}
		}
		io.Copy(io.Discard, pr)
	}()
	return io.MultiWriter(w, pw)
}

// files are the run records of kind in dir, newest first ("" for any).
func files(dir, kind string) []string {
	pattern := "*.json"
	if kind != "" {
		pattern = kind + "-*.json"
	}
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	sort.Slice(matches, func(i, j int) bool { return filepath.Base(matches[i]) > filepath.Base(matches[j]) })
	if kind == "" {
		// Kinds sort apart by name: order by the timestamp after the kind.
		sort.Slice(matches, func(i, j int) bool { return stamp(matches[i]) > stamp(matches[j]) })
	}
	return matches
}

func stamp(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	if i := strings.Index(base, "-"); i >= 0 {
		return base[i+1:]
	}
	return base
}

// prune keeps the newest runs of kind, with their events and logs.
func prune(dir, kind string) {
	for _, f := range files(dir, kind)[min(history, len(files(dir, kind))):] {
		if r, err := load(f); err == nil && r.Log != "" && filepath.Dir(r.Log) == dir {
			os.Remove(r.Log)
		}
		os.Remove(eventsPath(f))
		os.Remove(f)
	}
}

func load(path string) (*Run, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &r, nil
}

// estimates averages each phase's duration over the last three
// successful runs of kind that ran it.
func estimates(dir, kind string) map[string]time.Duration {
	sums, counts := map[string]time.Duration{}, map[string]int{}
	runs := 0
	for _, f := range files(dir, kind) {
		r, err := load(f)
		if err != nil || r.Result != ResultOK {
			continue
		}
		for _, p := range r.Phases {
			if p.State == Done && counts[p.ID] < 3 {
				sums[p.ID] += p.Took()
				counts[p.ID]++
			}
		}
		if runs++; runs == 3 {
			break
		}
	}
	out := map[string]time.Duration{}
	for id, n := range counts {
		out[id] = sums[id] / time.Duration(n)
	}
	return out
}

// Latest is the newest run recorded in dir, of any kind; nil if none.
func Latest(dir string) (*Run, error) {
	fs := files(dir, "")
	if len(fs) == 0 {
		return nil, nil
	}
	return load(fs[0])
}

// Stale reports whether a run said to be running stopped rewriting its
// record: the command is gone without finishing.
func (r *Run) Stale(now time.Time) bool {
	return r.Result == ResultRunning && now.Sub(r.Updated) > StaleAfter
}

// Remaining estimates how long the run still needs: the rest of the
// running phase's estimate and the estimates of the phases not begun.
func (r *Run) Remaining(now time.Time) time.Duration {
	var left time.Duration
	for _, p := range r.Phases {
		switch p.State {
		case Pending:
			left += time.Duration(p.Estimate)
		case Running:
			if rest := time.Duration(p.Estimate) - now.Sub(*p.Started); rest > 0 {
				left += rest
			}
		}
	}
	return left
}

// Print writes the run for a person: what it is, how long it has run or
// took, each phase with its time, and what is left.
func (r *Run) Print(w io.Writer, now time.Time) {
	switch {
	case r.Stale(now):
		fmt.Fprintf(w, "%s: stopped without finishing - no news since %s (started %s)\n", r.Command, r.Updated.Local().Format("15:04:05"), r.Started.Local().Format("2006-01-02 15:04"))
	case r.Result == ResultRunning:
		fmt.Fprintf(w, "%s: running for %s, about %s left (started %s)\n", r.Command, short(now.Sub(r.Started)), short(r.Remaining(now)), r.Started.Local().Format("15:04"))
	case r.Result == ResultOK:
		fmt.Fprintf(w, "%s: done in %s (%s)\n", r.Command, short(r.Ended.Sub(r.Started)), r.Started.Local().Format("2006-01-02 15:04"))
	default:
		fmt.Fprintf(w, "%s: failed after %s (%s)\n", r.Command, short(r.Ended.Sub(r.Started)), r.Started.Local().Format("2006-01-02 15:04"))
	}
	width := 0
	for _, p := range r.Phases {
		width = max(width, len(p.Title))
	}
	for _, p := range r.Phases {
		var mark, detail string
		switch p.State {
		case Done:
			mark, detail = "✓", short(p.Took())
		case Running:
			so := now.Sub(*p.Started)
			mark, detail = "▶", short(so)+" so far"
			if rest := time.Duration(p.Estimate) - so; rest > 0 {
				detail += ", about " + short(rest) + " left"
			} else {
				detail += ", longer than the usual " + short(time.Duration(p.Estimate))
			}
		case Failed:
			mark, detail = "✗", "failed after "+short(p.Took())
		case Skipped:
			mark, detail = "-", "skipped"
			if p.Note != "" {
				detail += ": " + p.Note
			}
		default:
			mark, detail = "·", "about "+short(time.Duration(p.Estimate))
		}
		fmt.Fprintf(w, "  %s %-*s  %s\n", mark, width, p.Title, detail)
	}
	if r.Error != "" {
		fmt.Fprintf(w, "Error: %s\n", r.Error)
	}
}

// short writes a duration as 45s, 3m10s or 1h02m.
func short(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
