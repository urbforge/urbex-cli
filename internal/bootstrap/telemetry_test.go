package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The telemetry agent's container carries its configuration's hash: a
// new config.alloy recreates it, an unchanged one leaves it alone.
func TestWriteTelemetryAgent_ConfigHashRecreatesTheContainer(t *testing.T) {
	label := regexp.MustCompile(`urbex.config: "([0-9a-f]{16})"`)
	hash := func(a TelemetryAgent) string {
		t.Helper()
		dir := t.TempDir()
		if err := WriteTelemetryAgent(dir, a, Endpoints{LokiPushURL: "http://loki", PrometheusWriteURL: "http://prom"}); err != nil {
			t.Fatal(err)
		}
		compose, _ := os.ReadFile(filepath.Join(dir, TelemetryDir, a.Host, "compose.yaml"))
		m := label.FindSubmatch(compose)
		if m == nil {
			t.Fatalf("no config hash label:\n%s", compose)
		}
		return string(m[1])
	}
	a := TelemetryAgent{Host: "shop-api-prod", Kind: AppTag, Service: "api", Project: "shop", Env: "prod", Logs: true}
	first := hash(a)
	if hash(a) != first {
		t.Error("the same configuration must keep the same hash")
	}
	a.Logs = false
	if hash(a) == first {
		t.Error("a new configuration must change the hash")
	}
}

// Each platform Stack's config.env holds its files' hash, so that the
// containers reading them are recreated when one changes.
func TestRun_PlatformStacksCarryTheirFilesHash(t *testing.T) {
	dir := t.TempDir()
	o, _, _ := newRunOrchestrator(t, &fakeProxmox{})
	if err := o.Run(context.Background(), Options{GitOpsRepoPath: dir}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	config, _ := os.ReadFile(filepath.Join(dir, "platform/observability/config.env"))
	if !regexp.MustCompile(`(?m)^` + StackFilesVar + `=[0-9a-f]{16}$`).Match(config) {
		t.Errorf("config.env lacks %s:\n%s", StackFilesVar, config)
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "platform/observability/compose.yaml"))
	if n := strings.Count(string(compose), "- config.env"); n < 2 {
		t.Errorf("Grafana and Prometheus both read config.env, %d services do:\n%s", n, compose)
	}
}
