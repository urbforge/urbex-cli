package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every catalog entry declares what the conventions ask (ADR-0027): a
// kind, how it is deployed, what it provides, where it runs, and an
// internal name and an exposure for each endpoint - or that it listens
// on nothing. A new service that doesn't fails here.
func TestCatalog_EveryServiceFollowsTheConventions(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Catalog {
		if s.Name == "" || seen[s.Name] {
			t.Errorf("catalog entry %+v: a unique name is required", s)
		}
		seen[s.Name] = true
		switch s.Kind {
		case KindPlatform:
			if s.LXC == "" {
				t.Errorf("%s: a platform service names the LXC it runs on", s.Name)
			}
		case KindAgent:
			if s.LXC != "" {
				t.Errorf("%s: an agent runs on every LXC, not on %s", s.Name, s.LXC)
			}
		default:
			t.Errorf("%s: kind %q, want platform or agent (apps come from AppService)", s.Name, s.Kind)
		}
		switch s.Deploy {
		case DeployFilesOnHost, DeployGitOps, DeployAnsible:
		default:
			t.Errorf("%s: deploy %q, want a Komodo Stack (files on host, GitOps) or Ansible", s.Name, s.Deploy)
		}
		if len(s.Provides) == 0 {
			t.Errorf("%s: declare what it provides", s.Name)
		}
		if len(s.Endpoints) == 0 && !s.OutboundOnly {
			t.Errorf("%s: declare its endpoints, or that it is outbound only", s.Name)
		}
		for _, e := range s.Endpoints {
			checkEndpoint(t, s, e)
		}
	}
	app := AppService("api", 8080, true)
	if app.Kind != KindApp || app.Endpoints[0].Exposure != ExposurePublic || AppService("api", 8080, false).Endpoints[0].Exposure != ExposureInternal {
		t.Errorf("app service = %+v", app)
	}
}

func checkEndpoint(t *testing.T, s Service, e Endpoint) {
	t.Helper()
	if e.Label == "" {
		t.Errorf("%s: endpoint %+v has no internal name", s.Name, e)
	}
	switch e.Exposure {
	case ExposurePublic, ExposureAccess, ExposureInternal:
	default:
		t.Errorf("%s: endpoint %+v: exposure %q, want public, access or internal", s.Name, e, e.Exposure)
	}
	if e.Port == 0 && !s.OutboundOnly {
		t.Errorf("%s: endpoint %+v has no port", s.Name, e)
	}
}

var (
	composeService = regexp.MustCompile(`^  ([a-z0-9][a-z0-9_-]*):\s*$`)
	kindLabel      = regexp.MustCompile(`^\s+urbex\.kind:\s*"?([a-z]+)"?`)
	serviceLabel   = regexp.MustCompile(`^\s+urbex\.service:\s*"?([a-z0-9-]+)"?`)
)

// Every container urbex runs - in the base services' and agents' Ansible
// compose templates and in the platform Stacks - carries the labels
// urbex.kind and urbex.service, and its service is in the catalog with
// that kind: a service added without a catalog entry fails here.
// (A project's containers are labelled app by envrepo, tested there.)
func TestCatalog_EveryContainerIsInTheCatalog(t *testing.T) {
	root := filepath.Join("..", "..", "platform")
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasPrefix(d.Name(), "compose.yaml") || strings.HasPrefix(d.Name(), "docker-compose.yml")) {
			files = append(files, p)
		}
		return nil
	})
	if len(files) < 10 {
		t.Fatalf("found only %d compose files under %s: %v", len(files), root, files)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for name, labels := range containers(string(data)) {
			kind, service := labels[0], labels[1]
			if kind == "" || service == "" {
				t.Errorf("%s: container %q lacks urbex.kind or urbex.service", f, name)
				continue
			}
			entry, ok := CatalogService(service)
			if !ok {
				t.Errorf("%s: container %q is service %q, which isn't in the catalog (internal/platform/catalog.go)", f, name, service)
				continue
			}
			if string(entry.Kind) != kind {
				t.Errorf("%s: container %q is %s, the catalog says %s is %s", f, name, kind, service, entry.Kind)
			}
		}
	}
}

// containers maps each service of a compose file to its urbex.kind and
// urbex.service labels.
func containers(compose string) map[string][2]string {
	out := map[string][2]string{}
	inServices, current := false, ""
	for _, line := range strings.Split(compose, "\n") {
		switch {
		case line == "services:":
			inServices = true
			continue
		case len(line) > 0 && line[0] != ' ' && line[0] != '#' && line[0] != '{':
			inServices = false
		}
		if !inServices {
			continue
		}
		if m := composeService.FindStringSubmatch(line); m != nil {
			current = m[1]
			out[current] = [2]string{}
			continue
		}
		if current == "" {
			continue
		}
		l := out[current]
		if m := kindLabel.FindStringSubmatch(line); m != nil {
			l[0] = m[1]
		}
		if m := serviceLabel.FindStringSubmatch(line); m != nil {
			l[1] = m[1]
		}
		out[current] = l
	}
	return out
}

// The reserved internal names come from the catalog.
func TestCatalog_DrivesTheReservedNames(t *testing.T) {
	want := "dns,git,komodo,hooks,auth,grafana,prometheus,loki,tunnel"
	var got []string
	for _, l := range PlatformLabels {
		got = append(got, l.Label)
	}
	if strings.Join(got, ",") != want {
		t.Errorf("PlatformLabels = %v, want %s", got, want)
	}
	if !ReservedLabel("grafana") || ReservedLabel("hello") {
		t.Error("ReservedLabel")
	}
}
