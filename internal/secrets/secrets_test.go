package secrets

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func testKey(t *testing.T) (secret, recipient string) {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return identity.String(), identity.Recipient().String()
}

func TestRecipient(t *testing.T) {
	secret, want := testKey(t)
	got, err := Recipient(secret + "\n")
	if err != nil || got != want {
		t.Errorf("Recipient = %q, %v; want %q", got, err, want)
	}
	if _, err := Recipient("age-key"); err == nil || !strings.Contains(err.Error(), "URBEX_AGE_KEY") {
		t.Errorf("expected a clear error for a malformed key, got %v", err)
	}
}

func TestWriteSopsConfig(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSopsConfig(dir, "age1abc"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".sops.yaml"))
	if !strings.Contains(string(data), "age: age1abc") || !strings.Contains(string(data), `secrets\.sops\.env$`) {
		t.Errorf("unexpected .sops.yaml:\n%s", data)
	}
}

func TestPlaceholderFileHasNoSecretsAndNeedsNoSops(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	// A key that can't decrypt anything, and possibly no sops at all: a
	// placeholder must be readable regardless.
	store := &Store{AgeKey: "unused"}
	values, err := store.Read(context.Background(), path)
	if err != nil || len(values) != 0 {
		t.Errorf("Read(placeholder) = %v, %v; want no secrets", values, err)
	}
	keys, err := Keys(path)
	if err != nil || len(keys) != 0 {
		t.Errorf("Keys(placeholder) = %v, %v; want none", keys, err)
	}

	// EnsureFile never overwrites.
	os.WriteFile(path, []byte("API_TOKEN=ENC[x]\nsops_version=3.13.3\n"), 0o644)
	if err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	keys, _ = Keys(path)
	if len(keys) != 1 || keys[0] != "API_TOKEN" {
		t.Errorf("Keys = %v, want only API_TOKEN (sops metadata is not a secret)", keys)
	}
}

func TestValidKey(t *testing.T) {
	for _, ok := range []string{"API_TOKEN", "db_password", "_X1"} {
		if !ValidKey(ok) {
			t.Errorf("ValidKey(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "1ABC", "A-B", "A B", "A=B", "sops_version"} {
		if ValidKey(bad) {
			t.Errorf("ValidKey(%q) = true", bad)
		}
	}
}

// The round trip needs the sops binary; the end-to-end test covers it
// where it isn't installed.
func TestStore_SetReadUnsetRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops not installed")
	}
	secret, _ := testKey(t)
	store, err := NewStore(secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	if err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}

	if err := store.Set(ctx, path, "API_TOKEN", "s3cr3t"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set(ctx, path, "DB_PASSWORD", "p=w d"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "s3cr3t") {
		t.Fatal("the secret is in the file in plaintext")
	}
	values, err := store.Read(ctx, path)
	if err != nil || values["API_TOKEN"] != "s3cr3t" || values["DB_PASSWORD"] != "p=w d" {
		t.Fatalf("Read = %v, %v", values, err)
	}

	for _, key := range []string{"API_TOKEN", "DB_PASSWORD"} {
		if removed, err := store.Unset(ctx, path, key); err != nil || !removed {
			t.Fatalf("Unset(%s) = %v, %v", key, removed, err)
		}
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != Placeholder {
		t.Errorf("expected the placeholder back after the last secret is removed, got:\n%s", raw)
	}
	if removed, _ := store.Unset(ctx, path, "API_TOKEN"); removed {
		t.Error("Unset of a missing secret should report false")
	}
}
