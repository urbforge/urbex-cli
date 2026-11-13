package komodo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// exitCodeMark is the line Periphery prints after a command run on a
// Terminal, with its exit code (komodo_client::entities::KOMODO_EXIT_CODE).
const exitCodeMark = "__KOMODO_EXIT_CODE:"

// StackShell is a shell in a container of a Stack, through a Komodo
// Terminal (`docker exec -it <container> sh` on its Periphery): what
// urbex runs in an application's container without a port, a client or a
// container of its own (ADR-0030). Its commands share the shell - a
// variable set by one is there for the next - until Close deletes the
// Terminal.
type StackShell struct {
	c              *Client
	stack, service string
	// name is the Terminal's.
	name    string
	started bool
}

// Shell is a shell in the container of the Stack's compose service
// service, named name.
func (c *Client) Shell(stack, service, name string) *StackShell {
	return &StackShell{c: c, stack: stack, service: service, name: name}
}

func (s *StackShell) target() map[string]any {
	return map[string]any{"type": "Stack", "params": map[string]any{"stack": s.stack, "service": s.service}}
}

// Run runs command, a single line - the shell reads it from a terminal -
// and returns what it printed, without carriage returns, and its exit
// code. The first command starts the shell afresh.
func (s *StackShell) Run(ctx context.Context, command string) (string, int, error) {
	if strings.ContainsAny(command, "\r\n") {
		return "", 0, fmt.Errorf("a command run on a terminal must be one line")
	}
	recreate := "Never"
	if !s.started {
		recreate = "Always"
	}
	body, err := json.Marshal(map[string]any{
		"target":   s.target(),
		"terminal": s.name,
		"command":  command,
		"init":     map[string]any{"command": "sh", "recreate": recreate},
	})
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.c.baseURL+"/terminal/execute", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", s.c.apiKey)
	req.Header.Set("X-Api-Secret", s.c.apiSecret)
	resp, err := s.c.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("running a command in %s: %w", s.stack, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("reading the output of a command in %s: %w", s.stack, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("komodo could not run a command in %s: %s: %s", s.stack, resp.Status, strings.TrimSpace(string(data)))
	}
	s.started = true
	return parseTerminalOutput(string(data))
}

// parseTerminalOutput splits what a Terminal streamed into the command's
// output and its exit code: Periphery starts it with an empty line and
// ends it with the exit code and its end mark.
func parseTerminalOutput(data string) (string, int, error) {
	var out []string
	code := -1
	for _, line := range strings.Split(strings.ReplaceAll(data, "\r", ""), "\n") {
		if rest, ok := strings.CutPrefix(line, exitCodeMark); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				return "", 0, fmt.Errorf("unreadable exit code %q", rest)
			}
			code = n
			break
		}
		out = append(out, line)
	}
	if code < 0 {
		return "", 0, fmt.Errorf("the terminal ended before the command did: %s", strings.TrimSpace(strings.Join(out, "\n")))
	}
	return strings.Trim(strings.Join(out, "\n"), "\n"), code, nil
}

// Close deletes the Terminal, and the shell with it.
func (s *StackShell) Close(ctx context.Context) error {
	if !s.started {
		return nil
	}
	s.started = false
	return s.c.call(ctx, "write", "DeleteTerminal", map[string]any{"target": s.target(), "terminal": s.name}, nil)
}
