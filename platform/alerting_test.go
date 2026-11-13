package assets

import (
	"io/fs"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const grafanaProvisioning = "stacks/observability/grafana/provisioning"

type alertRule struct {
	UID         string            `yaml:"uid"`
	Title       string            `yaml:"title"`
	Condition   string            `yaml:"condition"`
	NoDataState string            `yaml:"noDataState"`
	For         string            `yaml:"for"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
	Data        []struct {
		RefID         string `yaml:"refId"`
		DatasourceUID string `yaml:"datasourceUid"`
		Model         struct {
			Expr string `yaml:"expr"`
		} `yaml:"model"`
	} `yaml:"data"`
}

// Every provisioned alert rule reads a datasource Grafana has, has a
// unique uid and a condition among its queries.
func TestAlertRules_AreProvisionable(t *testing.T) {
	var ds struct {
		Datasources []struct {
			UID string `yaml:"uid"`
		} `yaml:"datasources"`
	}
	readYAML(t, grafanaProvisioning+"/datasources/urbex.yml", &ds)
	known := map[string]bool{"__expr__": true}
	for _, d := range ds.Datasources {
		known[d.UID] = true
	}
	uids := map[string]bool{}
	for _, r := range allRules(t) {
		if uids[r.UID] {
			t.Errorf("uid %s twice", r.UID)
		}
		uids[r.UID] = true
		refs := map[string]bool{}
		for _, d := range r.Data {
			refs[d.RefID] = true
			if !known[d.DatasourceUID] {
				t.Errorf("%s: unknown datasource %q", r.UID, d.DatasourceUID)
			}
		}
		if !refs[r.Condition] {
			t.Errorf("%s: condition %q is none of its queries", r.UID, r.Condition)
		}
	}
}

// The resource alerts (urbex-cli#91): disk and memory, with a severity,
// and silent for a machine that doesn't report.
func TestAlertRules_Resources(t *testing.T) {
	want := map[string]string{"urbex-disk-filling": "warning", "urbex-disk-full": "critical", "urbex-memory-low": "warning"}
	got := map[string]alertRule{}
	for _, r := range allRules(t) {
		got[r.UID] = r
	}
	for uid, severity := range want {
		r, ok := got[uid]
		if !ok {
			t.Errorf("no rule %s", uid)
			continue
		}
		if r.Labels["severity"] != severity {
			t.Errorf("%s: severity %q, want %q", uid, r.Labels["severity"], severity)
		}
		if r.NoDataState != "OK" {
			t.Errorf("%s: a machine that doesn't report raises nothing, noDataState is %q", uid, r.NoDataState)
		}
		expr := r.Data[0].Model.Expr
		for _, label := range []string{"kind", "project", "service", "env", "host"} {
			if !strings.Contains(expr, label) {
				t.Errorf("%s keeps no %s label: %s", uid, label, expr)
			}
		}
		if strings.HasPrefix(uid, "urbex-disk") && (!strings.Contains(expr, `fstype!~"tmpfs`) || !strings.Contains(expr, "mountpoint")) {
			t.Errorf("%s: every real filesystem, by mountpoint: %s", uid, expr)
		}
		if uid == "urbex-memory-low" && strings.Contains(expr, "MemAvailable") {
			t.Errorf("%s: MemAvailable is the telemetry container's, not the LXC's: %s", uid, expr)
		}
	}
}

// The LXC's memory comes from its root cgroup, which the telemetry
// agent keeps under its own names.
func TestTelemetry_KeepsTheLXCMemory(t *testing.T) {
	data, err := fs.ReadFile(Stacks, "stacks/telemetry/config.alloy.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`regex         = "container_memory_(usage_bytes|cache);/"`, `replacement   = "urbex_lxc_memory_$1"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config.alloy.tmpl lacks %s", want)
		}
	}
}

func allRules(t *testing.T) []alertRule {
	t.Helper()
	files, err := fs.Glob(Stacks, grafanaProvisioning+"/alerting/*.yml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no alerting files: %v", err)
	}
	var rules []alertRule
	for _, f := range files {
		var doc struct {
			Groups []struct {
				Rules []alertRule `yaml:"rules"`
			} `yaml:"groups"`
		}
		readYAML(t, f, &doc)
		for _, g := range doc.Groups {
			rules = append(rules, g.Rules...)
		}
	}
	return rules
}

func readYAML(t *testing.T, name string, v any) {
	t.Helper()
	data, err := fs.ReadFile(Stacks, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}
