package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/allocation"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/envrepo"
	"github.com/urbforge/urbex-cli/internal/manifest"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/provision"
	"github.com/urbforge/urbex-cli/internal/rollout"
)

// --output takes text or json, nothing else.
func TestOutputFlag(t *testing.T) {
	var o outputFlag
	cmd := &cobra.Command{Use: "x"}
	o.Register(cmd)
	if o.JSON() || o.String() != "text" {
		t.Errorf("default = %s, want text", o.String())
	}
	if err := cmd.Flags().Set("output", "yaml"); err == nil {
		t.Error("yaml should be refused")
	}
	if err := cmd.Flags().Set("output", "json"); err != nil || !o.JSON() {
		t.Errorf("json: %v, JSON() = %v", err, o.JSON())
	}
}

// The JSON of a DNS record says its state; the text stays as it was.
func TestDNSRecord(t *testing.T) {
	var d *dnsChecker
	r := d.Lookup(context.Background(), "api.urbex.lab", "10.0.0.5")
	if r.State != dnsNotChecked || r.String() != "dns=api.urbex.lab (not checked: no technitium token)" {
		t.Errorf("nil checker = %+v %q", r, r)
	}
	if d.Lookup(context.Background(), "", "10.0.0.5") != nil {
		t.Error("no name, no record")
	}
	for _, tc := range []struct {
		r    dnsRecord
		want string
	}{
		{dnsRecord{Name: "a", State: dnsOK, Expected: "1", PointsTo: []string{"1"}}, "dns=a"},
		{dnsRecord{Name: "a", State: dnsMissing, Expected: "1"}, "dns=a (missing)"},
		{dnsRecord{Name: "a", State: dnsElsewhere, Expected: "1", PointsTo: []string{"2"}}, "dns=a (points to 2, expected 1)"},
		{dnsRecord{Name: "a", State: dnsUnreachable}, "dns=a (technitium unreachable)"},
		{dnsRecord{Name: "a", State: dnsError, Detail: "403"}, "dns=a (technitium: 403)"},
	} {
		if got := tc.r.String(); got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.r, got, tc.want)
		}
	}
}

// The logins name the users and the keys of their passwords, never a
// password.
func TestLogins_NoPasswords(t *testing.T) {
	data, _ := json.Marshal(logins("/home/me/.urbex/credentials.yaml"))
	for _, want := range []string{`"credentialsFile":"/home/me/.urbex/credentials.yaml"`, `"passwordKey":"technitiumAdminPassword"`, `"user":"urbex-admin"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in %s", want, data)
		}
	}
	if strings.Contains(string(data), `"password"`) {
		t.Errorf("a password in %s", data)
	}
}

func TestReconcilerView(t *testing.T) {
	now := time.Now()
	if newReconcilerView(rollout.ReconcilerStatus{}, now) != nil {
		t.Error("an environment without links has no reconciler")
	}
	for _, tc := range []struct {
		last time.Time
		want string
	}{{time.Time{}, "never-run"}, {now.Add(-time.Hour), "stale"}, {now.Add(-30 * time.Second), "running"}} {
		v := newReconcilerView(rollout.ReconcilerStatus{Linked: true, LastRun: tc.last, Open: []string{"api"}}, now)
		if v.State != tc.want || (tc.last.IsZero() != (v.LastRun == nil)) || len(v.Open) != 1 {
			t.Errorf("last run %v: %+v, want %s", tc.last, v, tc.want)
		}
	}
}

// The projects of `urbex status --output json`: the same as the text,
// as fields - a web frontend has no LAN address, a service its DNS
// record's state.
func TestProjectsStatus_JSON(t *testing.T) {
	repo := envrepo.Repo{Path: t.TempDir()}
	if err := repo.WriteService("prod", "shop", manifest.Service{Name: "api", Runtime: "go", Port: 8080}, "reg/urbex/shop-api", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteService("prod", "site", manifest.Service{Name: manifest.WebService, Runtime: manifest.WebRuntime}, "reg/urbex/site-web", false, nil); err != nil {
		t.Fatal(err)
	}
	ledger, _ := allocation.Load(repo.Path+"/state/allocations.json", 0, 0)
	ledger.Entries = map[string]allocation.Entry{provision.Hostname("shop", "api", "prod"): {VMID: 9121, IP: "192.0.2.45"}}
	if err := ledger.Save(repo.Path + "/state/allocations.json"); err != nil {
		t.Fatal(err)
	}
	cfg := &platform.Config{Domain: "example.com", Cloudflare: platform.CloudflareConfig{AccountID: "a", ZoneID: "z", Name: "urbex"}}
	var warnings []string
	views, err := projectsStatus(context.Background(), func(w string) { warnings = append(warnings, w) }, cfg, &credentials.Credentials{}, repo.Path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeJSON(&out, views); err != nil {
		t.Fatal(err)
	}
	var got []projectView
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Project != "shop" || got[1].Project != "site" {
		t.Fatalf("projects = %+v", got)
	}
	api := got[0].Services[0]
	if api.Service != "api" || api.LAN != "http://192.0.2.45:8080" || api.DNS == nil || api.DNS.State != dnsNotChecked || api.Web {
		t.Errorf("shop api = %+v", api)
	}
	web := got[1].Services[0]
	if !web.Web || web.LAN != "" || web.Public != "https://site-urbex.example.com" {
		t.Errorf("site web = %+v", web)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
}
