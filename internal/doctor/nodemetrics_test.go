package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/urbforge/urbex-cli/internal/platform"
)

type fakePerms struct {
	perms map[string]map[string]bool
	err   error
}

func (f fakePerms) Permissions(context.Context) (map[string]map[string]bool, error) {
	return f.perms, f.err
}

func TestNodeMetricsChecks(t *testing.T) {
	ctx := context.Background()
	cfg := &platform.Config{}
	token := "urbex-monitor@pve!metrics=00000000-1111-2222-3333-444444444444"
	check := func(m PermissionsReader) Check {
		r := &Report{}
		NodeMetricsChecks(ctx, m, token, cfg, r)
		if len(r.Checks) != 1 {
			t.Fatalf("checks: %+v", r.Checks)
		}
		return r.Checks[0]
	}
	if c := check(nil); c.Status != Skip || !strings.Contains(c.Detail, "off") || !strings.Contains(c.Fix, "proxmoxMonitorToken") {
		t.Errorf("no token: %+v", c)
	}
	if c := check(fakePerms{perms: map[string]map[string]bool{"/": {"Sys.Audit": true, "Datastore.Audit": true, "VM.Audit": true}}}); c.Status != OK {
		t.Errorf("auditor: %+v", c)
	}
	if c := check(fakePerms{perms: map[string]map[string]bool{"/pool/urbex": {"Sys.Audit": true}}}); c.Status != Fail || !strings.Contains(c.Detail, "Sys.Audit, Datastore.Audit") || !strings.Contains(c.Fix, "PVEAuditor") {
		t.Errorf("pool-only token: %+v", c)
	}
	if c := check(fakePerms{err: errors.New("401 authentication failure")}); c.Status != Fail || !strings.Contains(c.Detail, "401") {
		t.Errorf("bad token: %+v", c)
	}
	// Only the secret, as 'pveum user token add' prints it on its own line.
	token = "00000000-1111-2222-3333-444444444444"
	if c := check(fakePerms{err: errors.New("401")}); c.Status != Fail || !strings.Contains(c.Detail, "token's id") || !strings.Contains(c.Fix, "urbex-monitor@pve!metrics=") {
		t.Errorf("secret only: %+v", c)
	}
	r := &Report{}
	NodeMetricsChecks(ctx, nil, "", nil, r)
	if len(r.Checks) != 0 {
		t.Error("checked without a config")
	}
}
