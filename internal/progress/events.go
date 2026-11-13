package progress

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// Event is one thing that happened in a run, as a JSON line: what
// `--events json` prints and the run's events file keeps, so that a
// program - the setup UI, the MCP server, the plugin - follows a run, and
// follows it again after a disconnect (ADR-0032).
type Event struct {
	Time    time.Time `json:"time"`
	Run     string    `json:"run"`  // the run's ID: its record's name
	Kind    string    `json:"kind"` // bootstrap, apply, destroy, teardown
	Type    string    `json:"type"` // see the Event* constants
	Command string    `json:"command,omitempty"`
	Phase   string    `json:"phase,omitempty"`
	Title   string    `json:"title,omitempty"`
	Step    string    `json:"step,omitempty"`
	// Estimate is a starting phase's expected duration, Seconds a
	// finished phase's or run's actual one.
	Estimate *Seconds `json:"estimate,omitempty"`
	Seconds  *Seconds `json:"seconds,omitempty"`
	Note     string   `json:"note,omitempty"`
	Result   string   `json:"result,omitempty"` // run-end: ok or failed
	Error    string   `json:"error,omitempty"`
	Log      string   `json:"log,omitempty"` // run-start: where the output goes
}

// The types of events, in the order a run has them.
const (
	EventRunStart   = "run-start"
	EventPhaseStart = "phase-start"
	EventStep       = "step" // an Ansible task, a machine Terraform is done with
	EventPhaseEnd   = "phase-end"
	EventPhaseSkip  = "phase-skip"
	EventPhaseFail  = "phase-fail"
	EventRunEnd     = "run-end"
)

func secondsOf(d time.Duration) *Seconds {
	s := Seconds(d)
	return &s
}

// eventsPath is the run's events file, next to its record.
func eventsPath(record string) string {
	return strings.TrimSuffix(record, ".json") + ".events"
}

// emit appends an event to the run's events file and writes it to the
// live writer, if any. Called with t.mu held, or before the tracker is
// shared. Errors are ignored: events only report.
func (t *Tracker) emit(e Event) {
	e.Time, e.Run, e.Kind = t.now(), runID(t.path), t.run.Kind
	if e.Type == EventRunStart {
		e.Command = t.run.Command
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')
	if f, err := os.OpenFile(eventsPath(t.path), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		f.Write(line)
		f.Close()
	}
	if t.live != nil {
		t.live.Write(line)
	}
}

// Step records a step of the running phase.
func (t *Tracker) Step(step string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	phase := ""
	for _, p := range t.run.Phases {
		if p.State == Running {
			phase = p.ID
		}
	}
	t.emit(Event{Type: EventStep, Phase: phase, Step: step})
}

func runID(record string) string {
	base := record[strings.LastIndexAny(record, `/\`)+1:]
	return strings.TrimSuffix(base, ".json")
}

var (
	// ansi is a terminal escape sequence: Terraform colours its output.
	ansi       = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
	taskHeader = regexp.MustCompile(`^TASK \[(.+?)\]`)
	// What Terraform says of a machine it is done with.
	terraformDone = regexp.MustCompile(`^(module\.\S+|proxmox_\S+): (Creation|Modifications|Destruction) complete after (\S+)`)
)

// stepOf is the step a line of Terraform's or Ansible's output reports,
// or "".
func stepOf(line string) string {
	line = ansi.ReplaceAllString(line, "")
	if m := taskHeader.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	if m := terraformDone.FindStringSubmatch(line); m != nil {
		return m[1] + ": " + strings.ToLower(m[2]) + " complete after " + m[3]
	}
	return ""
}

// Follow writes the events of the run whose record is at record to w,
// from the first: all of them, then - with follow - each new one until
// the run ends, stops sending its heartbeat, or ctx is done. A run that
// stopped without finishing gets a run-end event with result "stopped".
func Follow(ctx context.Context, record string, w io.Writer, follow bool, poll time.Duration) error {
	path := eventsPath(record)
	var f *os.File
	var offset int64
	for {
		if f == nil {
			f, _ = os.Open(path)
			if f != nil {
				defer f.Close()
			}
		}
		ended := false
		if f != nil {
			f.Seek(offset, io.SeekStart)
			r := bufio.NewReader(f)
			for {
				line, err := r.ReadBytes('\n')
				if err != nil {
					break // a line not finished yet is read again next time
				}
				offset += int64(len(line))
				if _, err := w.Write(line); err != nil {
					return err
				}
				var e Event
				if json.Unmarshal(line, &e) == nil && e.Type == EventRunEnd {
					ended = true
				}
			}
		}
		if ended || !follow {
			return nil
		}
		if run, err := load(record); err == nil && run.Stale(time.Now()) {
			e := Event{Time: time.Now(), Run: runID(record), Kind: run.Kind, Type: EventRunEnd, Result: "stopped",
				Error: "the command stopped without finishing: no news since " + run.Updated.Format(time.RFC3339)}
			line, _ := json.Marshal(e)
			_, err := w.Write(append(line, '\n'))
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
}

// LatestRecord is the newest run's record in dir, "" if none.
func LatestRecord(dir string) string {
	fs := files(dir, "")
	if len(fs) == 0 {
		return ""
	}
	return fs[0]
}
