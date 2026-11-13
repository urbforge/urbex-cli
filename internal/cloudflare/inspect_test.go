package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An account token is verified against the account; a user token, which
// the account endpoint refuses, against the user.
func TestVerifyToken(t *testing.T) {
	for _, accountToken := range []bool{true, false} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/accounts/acc/tokens/verify" && accountToken,
				r.URL.Path == "/user/tokens/verify" && !accountToken:
				w.Write([]byte(`{"success":true,"result":{"status":"active"}}`))
			default:
				w.WriteHeader(401)
				w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`))
			}
		}))
		status, err := New(server.URL, "t", "acc", "").VerifyToken(context.Background())
		if err != nil || status != "active" {
			t.Errorf("account token %v: %q, %v", accountToken, status, err)
		}
		server.Close()
	}
}

func TestZones(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones" || r.URL.Query().Get("account.id") != "acc" {
			t.Errorf("unexpected %s", r.URL)
		}
		w.Write([]byte(`{"success":true,"result":[{"id":"z1","name":"example.com","status":"active"}]}`))
	}))
	defer server.Close()
	zones, err := New(server.URL, "t", "acc", "").Zones(context.Background())
	if err != nil || len(zones) != 1 || zones[0].Name != "example.com" {
		t.Errorf("Zones = %+v, %v", zones, err)
	}
}
