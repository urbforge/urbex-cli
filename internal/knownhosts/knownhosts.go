// Package knownhosts keeps the SSH host keys of the platform's LXCs in the
// GitOps repo (state/known_hosts), so Ansible trusts each LXC from its
// first connection on, on every machine that runs urbex - and urbex, which
// creates and destroys the LXCs, knows when an address gets a new host.
package knownhosts

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Connection settings for Ansible's ssh to the LXCs. An LXC created at an
// address another one had answers only once the operator's machine and
// the network have forgotten the old one's MAC address, which can take
// longer than Ansible's default 10-second connect timeout: wait longer,
// and retry.
const (
	ConnectTimeout = "30"
	ConnectRetries = "5"
)

// Path is the known_hosts file of the GitOps repo at repoPath.
func Path(repoPath string) string {
	return filepath.Join(repoPath, "state", "known_hosts")
}

// SSHArgs are the ssh options Ansible connects to the LXCs with: an LXC
// seen for the first time is trusted and recorded, in plain text so that
// Forget can find it; a known one whose key changed is refused.
func SSHArgs(repoPath string) (string, error) {
	abs, err := filepath.Abs(Path(repoPath))
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(abs, `"'`) {
		return "", fmt.Errorf("the gitops repo path %q can't contain quotes", abs)
	}
	return fmt.Sprintf(`-o StrictHostKeyChecking=accept-new -o HashKnownHosts=no -o UserKnownHostsFile="%s"`, abs), nil
}

// Forget removes the host keys recorded for the given addresses: their
// LXC was just created or destroyed, so whatever key was there belonged
// to a machine that no longer exists.
func Forget(repoPath string, addrs ...string) error {
	path := Path(repoPath)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) || len(addrs) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	drop := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		drop[a] = true
	}
	var kept bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if matches(line, drop) {
			continue
		}
		kept.WriteString(line + "\n")
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return os.WriteFile(path, kept.Bytes(), 0o644)
}

// matches reports whether a known_hosts line is for one of the hosts:
// its first field is a comma-separated list of names, each either a host
// or [host]:port.
func matches(line string, hosts map[string]bool) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
		return false
	}
	for _, name := range strings.Split(fields[0], ",") {
		if strings.HasPrefix(name, "[") {
			if end := strings.Index(name, "]"); end > 0 {
				name = name[1:end]
			}
		}
		if hosts[name] {
			return true
		}
	}
	return false
}

// Ensure creates the known_hosts file if it doesn't exist yet: ssh records
// new hosts in it, but doesn't create its directory.
func Ensure(repoPath string) error {
	path := Path(repoPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
