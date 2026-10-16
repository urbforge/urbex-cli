package keycloak

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// keycloakStarting answers 503 for the first n requests, then serves the
// master realm.
func keycloakStarting(n int32) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realms/master" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) <= n {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"realm":"master"}`))
	}))
	return srv, &calls
}

func TestWaitReady_Ready(t *testing.T) {
	srv, _ := keycloakStarting(0)
	defer srv.Close()
	waited := 0
	if err := WaitReady(context.Background(), srv.Client(), srv.URL, time.Second, time.Millisecond, func() { waited++ }); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if waited != 0 {
		t.Error("a ready Keycloak needs no waiting message")
	}
}

func TestWaitReady_Slow(t *testing.T) {
	srv, calls := keycloakStarting(3)
	defer srv.Close()
	waited := 0
	if err := WaitReady(context.Background(), srv.Client(), srv.URL, time.Second, time.Millisecond, func() { waited++ }); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if waited != 1 || calls.Load() != 4 {
		t.Errorf("waiting message %d times (want once), %d tries (want 4)", waited, calls.Load())
	}
}

func TestWaitReady_Timeout(t *testing.T) {
	srv, _ := keycloakStarting(1 << 30)
	defer srv.Close()
	err := WaitReady(context.Background(), srv.Client(), srv.URL, 20*time.Millisecond, 5*time.Millisecond, nil)
	if err == nil || !strings.Contains(err.Error(), srv.URL) || !strings.Contains(err.Error(), "503") {
		t.Fatalf("WaitReady = %v, want a timeout naming Keycloak and its last answer", err)
	}
}

func TestWaitReady_NotListeningYet(t *testing.T) {
	srv, _ := keycloakStarting(0)
	url := srv.URL
	srv.Close() // nothing listens: connection refused, as before Keycloak's container is up
	err := WaitReady(context.Background(), http.DefaultClient, url, 20*time.Millisecond, 5*time.Millisecond, nil)
	if err == nil || !strings.Contains(err.Error(), "didn't answer within") {
		t.Fatalf("WaitReady = %v, want a timeout", err)
	}
}
