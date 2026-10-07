package technitium

import (
	"context"
	"testing"

	"github.com/urbforge/urbex-cli/internal/technitium/technitiumtest"
)

func newTestClient(t *testing.T) (*Client, *technitiumtest.Server) {
	t.Helper()
	fake := technitiumtest.NewServer()
	t.Cleanup(fake.Close)
	return New(fake.URL, "tok"), fake
}

func TestCreateToken(t *testing.T) {
	ctx := context.Background()
	fake := technitiumtest.NewServer()
	t.Cleanup(fake.Close)
	fake.Token = "minted-token"

	c := New(fake.URL, "") // no token yet: the one call that doesn't need one
	token, err := c.CreateToken(ctx, "admin", "secret", "urbex-cli")
	if err != nil {
		t.Fatal(err)
	}
	if token != "minted-token" {
		t.Errorf("token = %q, want %q", token, "minted-token")
	}
}

func TestEnsureZone_IdempotentOnAlreadyExists(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)

	if err := c.EnsureZone(ctx, "urbex.lab"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureZone(ctx, "urbex.lab"); err != nil {
		t.Fatalf("a second EnsureZone must tolerate the zone already existing: %v", err)
	}
}

func TestSetRecord_OverwritesAndDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	c, fake := newTestClient(t)
	if err := c.EnsureZone(ctx, "urbex.lab"); err != nil {
		t.Fatal(err)
	}

	if err := c.SetRecord(ctx, "urbex.lab", "api-staging-demo-urbex.urbex.lab", "192.168.1.210"); err != nil {
		t.Fatal(err)
	}
	if got := fake.RecordIP("urbex.lab", "api-staging-demo-urbex.urbex.lab"); got != "192.168.1.210" {
		t.Fatalf("record IP = %q, want %q", got, "192.168.1.210")
	}
	// Redeploying with a new allocated IP overwrites, doesn't duplicate.
	if err := c.SetRecord(ctx, "urbex.lab", "api-staging-demo-urbex.urbex.lab", "192.168.1.211"); err != nil {
		t.Fatal(err)
	}
	if got := fake.RecordIP("urbex.lab", "api-staging-demo-urbex.urbex.lab"); got != "192.168.1.211" {
		t.Fatalf("record IP after overwrite = %q, want %q", got, "192.168.1.211")
	}

	if err := c.DeleteRecord(ctx, "urbex.lab", "api-staging-demo-urbex.urbex.lab", "192.168.1.211"); err != nil {
		t.Fatal(err)
	}
	if got := fake.RecordIP("urbex.lab", "api-staging-demo-urbex.urbex.lab"); got != "" {
		t.Fatalf("record should be gone, got IP %q", got)
	}
	// A second delete (destroy re-run, or a service that was never
	// registered) must not fail.
	if err := c.DeleteRecord(ctx, "urbex.lab", "api-staging-demo-urbex.urbex.lab", "192.168.1.211"); err != nil {
		t.Fatalf("a second DeleteRecord must tolerate the record being gone: %v", err)
	}
}

func TestAPIError(t *testing.T) {
	err := &APIError{Status: 200, Message: "Zone already exists: urbex.lab"}
	if !AlreadyExists(err) {
		t.Error("AlreadyExists should match")
	}
	if NotFound(err) {
		t.Error("NotFound should not match an already-exists error")
	}
	notFound := &APIError{Status: 200, Message: "Cannot delete record: no such record exists."}
	if !NotFound(notFound) {
		t.Error("NotFound should match")
	}
}

func TestGetRecords(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	if err := c.EnsureZone(ctx, "urbex.lab"); err != nil {
		t.Fatal(err)
	}

	ips, err := c.GetRecords(ctx, "urbex.lab", "api.urbex.lab")
	if err != nil || len(ips) != 0 {
		t.Fatalf("GetRecords before any record = %v, %v; want none", ips, err)
	}

	if err := c.SetRecord(ctx, "urbex.lab", "api.urbex.lab", "192.168.1.210"); err != nil {
		t.Fatal(err)
	}
	ips, err = c.GetRecords(ctx, "urbex.lab", "api.urbex.lab")
	if err != nil || len(ips) != 1 || ips[0] != "192.168.1.210" {
		t.Fatalf("GetRecords = %v, %v; want [192.168.1.210]", ips, err)
	}

	if _, err := c.GetRecords(ctx, "nope.lab", "api.nope.lab"); err == nil {
		t.Error("GetRecords in a zone that doesn't exist must fail")
	}
}
