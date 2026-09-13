package bootstrap

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// Command describes an external command to run during bootstrap
// (terraform, ansible-playbook, ...).
type Command struct {
	Dir  string
	Name string
	Args []string
	// Env holds additional "KEY=VALUE" entries appended to the current
	// process's environment - used to pass secrets like
	// TF_VAR_proxmox_api_token without writing them to a file.
	Env []string
}

// CommandRunner executes external commands. Abstracted so bootstrap's
// orchestration logic can be unit tested without actually invoking
// terraform/ansible-playbook.
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
	cmd.Stderr = r.Stderr
	return cmd.Run()
}
