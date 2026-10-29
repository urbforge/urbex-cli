// Package runner abstracts running external commands (terraform,
// ansible-playbook) so orchestration logic in internal/bootstrap and
// internal/provision can be unit tested without invoking real binaries.
package runner

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// Command describes an external command to run.
type Command struct {
	Dir  string
	Name string
	Args []string
	// Env holds additional "KEY=VALUE" entries appended to the current
	// process's environment - used to pass secrets like
	// TF_VAR_proxmox_api_token without writing them to a file.
	Env []string
	// Stdout, if set, takes the command's output instead of the
	// runner's: for output urbex reads, like terraform show -json.
	Stdout io.Writer
}

// CommandRunner executes external commands.
type CommandRunner interface {
	Run(ctx context.Context, cmd Command) error
}

// ExecRunner runs commands via os/exec, streaming their output to the
// given writers.
type ExecRunner struct {
	Stdout io.Writer
	Stderr io.Writer
}

func (r ExecRunner) Run(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdout = r.Stdout
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	}
	cmd.Stderr = r.Stderr
	return cmd.Run()
}
