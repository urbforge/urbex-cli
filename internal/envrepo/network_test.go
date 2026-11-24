package envrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/urbforge/urbex-cli/internal/manifest"
)

type composeFile struct {
	Services map[string]struct {
		NetworkMode string            `yaml:"network_mode"`
		Ports       []string          `yaml:"ports"`
		CapAdd      []string          `yaml:"cap_add"`
		Environment map[string]string `yaml:"environment"`
		Volumes     []string          `yaml:"volumes"`
		Command     []string          `yaml:"command"`
	} `yaml:"services"`
}

func vpnPair() (gluetun, qbit manifest.Service) {
	qbit = manifest.Service{Name: "qbittorrent", Image: "linuxserver/qbittorrent", Version: "5", Port: 8080, LXC: "download", Network: "gluetun", Uses: []string{"gluetun"}}
	gluetun = manifest.Service{Name: "gluetun", Image: "qmcgaw/gluetun", Version: "3", Port: 8000, Healthcheck: "/", LXC: "download", Capabilities: []string{"NET_ADMIN"}, Joiners: []manifest.Service{qbit}}
	return gluetun, qbit
}

func readCompose(t *testing.T, repo Repo, project, service string) composeFile {
	t.Helper()
	raw := read(t, filepath.Join(repo.Path, "environments/prod", project, service, ComposeFile))
	var c composeFile
	if err := yaml.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("%s's compose isn't YAML: %v\n%s", service, err, raw)
	}
	return c
}

// The application that joins has no network of its own; the other's
// container publishes both ports, has NET_ADMIN, and a rejoin service
// that watches it (urbforge/urbex-cli#139).
func TestCompose_NetworkJoined(t *testing.T) {
	repo := Repo{Path: t.TempDir()}
	gluetun, qbit := vpnPair()
	for _, svc := range []manifest.Service{gluetun, qbit} {
		if err := repo.WriteService("prod", "media", svc, svc.Image, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	q := readCompose(t, repo, "media", "qbittorrent").Services["app"]
	if q.NetworkMode != "container:media-gluetun-prod-app-1" || len(q.Ports) != 0 {
		t.Errorf("qbittorrent: network_mode %q, ports %v", q.NetworkMode, q.Ports)
	}
	g := readCompose(t, repo, "media", "gluetun")
	app := g.Services["app"]
	if !slices.Equal(app.Ports, []string{"8000:8000", "8080:8080"}) || !slices.Equal(app.CapAdd, []string{"NET_ADMIN"}) || app.NetworkMode != "" {
		t.Errorf("gluetun: ports %v, cap_add %v, network_mode %q", app.Ports, app.CapAdd, app.NetworkMode)
	}
	rejoin, ok := g.Services["rejoin"]
	if !ok {
		t.Fatal("gluetun's compose has no rejoin service")
	}
	if rejoin.Environment["TARGET"] != "media-gluetun-prod-app-1" || rejoin.Environment["JOINERS"] != "media-qbittorrent-prod-app-1" ||
		!slices.Contains(rejoin.Volumes, "/var/run/docker.sock:/var/run/docker.sock") {
		t.Errorf("rejoin: %+v", rejoin)
	}
	if _, ok := readCompose(t, repo, "media", "qbittorrent").Services["rejoin"]; ok {
		t.Error("only the application others join has a rejoin service")
	}
}

// The rejoin script, run by sh with a fake docker: a joiner started
// before the target's last start, or stopped, is restarted; one started
// after it, or missing, is left alone; nothing happens while the target
// isn't healthy.
func TestRejoinScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	// state/<container>/{health,started,running}; docker logs its
	// restarts to calls.
	fake := `#!/bin/sh
s=` + dir + `/state
case "$1" in
inspect)
  if [ "$2" = -f ]; then c=$4; [ -d "$s/$c" ] || exit 1
    case "$3" in
    *Health*) cat "$s/$c/health" ;;
    *StartedAt*) cat "$s/$c/started" ;;
    *Running*) cat "$s/$c/running" ;;
    esac
  else [ -d "$s/$2" ] || exit 1; fi ;;
restart) echo "restart $2" >> ` + dir + `/calls ;;
events) exit 0 ;;
esac
`
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	container := func(name, health, started, running string) {
		d := filepath.Join(dir, "state", name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, v := range map[string]string{"health": health, "started": started, "running": running} {
			if err := os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Compose's $$ is a literal $ for the shell.
	script := strings.ReplaceAll(rejoinScript, "$$", "$")
	run := func() string {
		t.Helper()
		os.Remove(filepath.Join(dir, "calls"))
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "TARGET=vpn", "JOINERS=old fresh stopped missing"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("rejoin script: %v\n%s", err, out)
		}
		calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
		return string(calls)
	}
	container("vpn", "starting", "2026-11-24T10:00:05.123456789Z", "true")
	container("old", "", "2026-11-24T09:00:00.5Z", "true")
	container("fresh", "", "2026-11-24T10:00:07.1Z", "true")
	container("stopped", "", "2026-11-24T10:00:09Z", "false")
	if calls := run(); calls != "" {
		t.Errorf("the target isn't healthy yet: nothing to do, got %q", calls)
	}
	container("vpn", "healthy", "2026-11-24T10:00:05.123456789Z", "true")
	if calls := run(); calls != "restart old\nrestart stopped\n" {
		t.Errorf("expected old and stopped restarted, got %q", calls)
	}
}
