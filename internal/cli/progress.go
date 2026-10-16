package cli

import (
	"fmt"
	"io"
	"path/filepath"
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

// startProgress begins recording a command's progress. Progress is only
// a help: if it can't be recorded, the command runs anyway, with a
// warning, and a nil Tracker.
func startProgress(cmd *cobra.Command, kind, command string, phases []progress.PhaseSpec) *progress.Tracker {
	dir, err := progressDir()
	if err == nil {
		var t *progress.Tracker
		if t, err = progress.Start(dir, kind, command, phases); err == nil {
			return t
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Warning: not recording progress (%v): 'urbex status --progress' won't show this run.\n", err)
	return nil
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
