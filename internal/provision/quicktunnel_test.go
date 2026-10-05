package provision

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQuickTunnelURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/quicktunnel" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hostname":"random-words.trycloudflare.com","id":"abc"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := quickTunnelURL(ctx, srv.URL+"/quicktunnel")
	if err != nil {
		t.Fatalf("quickTunnelURL: %v", err)
	}
	if want := "https://random-words.trycloudflare.com"; got != want {
		t.Errorf("quickTunnelURL = %q, want %q", got, want)
	}
}

func TestQuickTunnelURL_NotReadyThenReady(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls < 3 {
			w.Write([]byte(`{"hostname":""}`))
			return
		}
		w.Write([]byte(`{"hostname":"eventually.trycloudflare.com"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := quickTunnelURL(ctx, srv.URL+"/quicktunnel")
	if err != nil {
		t.Fatalf("quickTunnelURL: %v", err)
	}
	if want := "https://eventually.trycloudflare.com"; got != want {
		t.Errorf("quickTunnelURL = %q, want %q", got, want)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 calls (retry until ready), got %d", calls)
	}
}

func TestQuickTunnelURL_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hostname":""}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if _, err := quickTunnelURL(ctx, srv.URL+"/quicktunnel"); err == nil {
		t.Error("expected an error once the context's deadline passes without a hostname")
	}
}

func TestQuickTunnelURL_Unreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if _, err := quickTunnelURL(ctx, "http://127.0.0.1:1/quicktunnel"); err == nil {
		t.Error("expected an error when nothing listens on the metrics port")
	}
}
