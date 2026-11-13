package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/progress"
)

// progressDir is where bootstrap and apply record their progress, next to
// the credentials: per operator, never in the GitOps repo.
func progressDir() (string, error) {
	path, err := credentials.DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "progress"), nil
}

// startProgress begins recording a command's progress, with its events
// going to ev's stream when --events json is given. Progress is only a
// help: if it can't be recorded, the command runs anyway, with a
// warning, and a nil Tracker.
func startProgress(cmd *cobra.Command, kind, command string, phases []progress.PhaseSpec, ev *eventsMode) *progress.Tracker {
	dir, err := progressDir()
	if err == nil {
		var t *progress.Tracker
		opts := progress.Options{}
		if ev != nil {
			opts.Log, opts.Live = ev.log, ev.live
		}
		if t, err = progress.StartWith(dir, kind, command, phases, opts); err == nil {
			return t
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Warning: not recording progress (%v): 'urbex status --progress' won't show this run.\n", err)
	return nil
}

// eventsFlag is --events: json makes a long command print its progress
// as JSON lines on standard output (ADR-0032).
type eventsFlag struct{ value string }

func (e *eventsFlag) Register(cmd *cobra.Command) {
	cmd.Flags().Var(e, "events", "json: print the run's progress as JSON lines, and its output to a log file")
}
func (e *eventsFlag) String() string { return e.value }
func (e *eventsFlag) Set(v string) error {
	if v != "json" {
		return fmt.Errorf("must be json")
	}
	e.value = v
	return nil
}
func (e *eventsFlag) Type() string { return "format" }

// eventsMode is a run with --events json: its events go to live, the
// original standard output; everything it prints, Terraform's and
// Ansible's output included, goes to the log file instead.
type eventsMode struct {
	live io.Writer
	log  string
	file *os.File
}

// begin switches cmd to events mode when --events json was given, and
// returns nil otherwise. The run no longer stops when the terminal or
// the reader of its events goes away: it goes on in the background, and
// 'urbex status --progress --events json --watch' follows it again.
// Call it before anything writes to cmd's output.
func (e *eventsFlag) begin(cmd *cobra.Command, kind string) (*eventsMode, error) {
	if e.value == "" {
		return nil, nil
	}
	dir, err := progressDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.log", kind, time.Now().UTC().Format("20060102-150405.000")))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	m := &eventsMode{live: cmd.OutOrStdout(), log: path, file: f}
	cmd.SetOut(f)
	cmd.SetErr(f)
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	return m, nil
}

// close ends events mode; the command's error, if any, also goes to the
// log, while main prints it on standard error.
func (m *eventsMode) close(err error) {
	if m == nil {
		return
	}
	if err != nil {
		fmt.Fprintf(m.file, "Error: %v\n", err)
	}
	m.file.Close()
}

// showProgress prints the latest bootstrap or apply; with watch, again
// every interval until it ends.
func showProgress(cmd *cobra.Command, out io.Writer, watch bool, interval time.Duration) error {
	dir, err := progressDir()
	if err != nil {
		return err
	}
	for {
		run, err := progress.Latest(dir)
		if err != nil {
			return err
		}
		if run == nil {
			fmt.Fprintln(out, "No bootstrap or apply recorded yet on this machine.")
			return nil
		}
		if watch {
			fmt.Fprint(out, "\033[H\033[2J") // clear the screen
		}
		now := time.Now()
		run.Print(out, now)
		if !watch || run.Result != progress.ResultRunning || run.Stale(now) {
			return nil
		}
		select {
		case <-cmd.Context().Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// followEvents prints the latest run's events as JSON lines, from the
// first; with watch, each new one until the run ends.
func followEvents(cmd *cobra.Command, out io.Writer, watch bool, interval time.Duration) error {
	dir, err := progressDir()
	if err != nil {
		return err
	}
	record := progress.LatestRecord(dir)
	if record == "" {
		return fmt.Errorf("no bootstrap, apply, destroy or teardown recorded yet on this machine")
	}
	return progress.Follow(cmd.Context(), record, out, watch, interval)
}
