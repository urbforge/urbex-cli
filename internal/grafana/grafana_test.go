package grafana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTestContactPoint(t *testing.T) {
	var tested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "admin" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == receiversPath:
			w.Write([]byte(`{"items":[{"metadata":{"name":"ZW1wdHk"},"spec":{"title":"empty","integrations":[]}},
				{"metadata":{"name":"dXJiZXg"},"spec":{"title":"urbex","integrations":[
					{"uid":"urbex-telegram","type":"telegram","settings":{"chatid":"-1"},"secureFields":{"bottoken":true}},
					{"uid":"urbex-webhook","type":"webhook","settings":{},"secureFields":{"url":true}}]}}]}`))
		case r.Method == http.MethodPost && r.URL.Path == receiversPath+"/dXJiZXg/test":
			var body struct {
				Integration struct {
					UID          string          `json:"uid"`
					SecureFields map[string]bool `json:"secureFields"`
				}
				Alert struct{ Annotations map[string]string }
			}
			json.NewDecoder(r.Body).Decode(&body)
			if len(body.Integration.SecureFields) == 0 || body.Alert.Annotations["summary"] != "hello" {
				t.Errorf("test body: %+v", body)
			}
			tested = append(tested, body.Integration.UID)
			if body.Integration.UID == "urbex-telegram" {
				w.Write([]byte(`{"status":"failure","error":"failed to send telegram message: webhook response status 401 Unauthorized"}`))
				return
			}
			w.Write([]byte(`{"status":"success"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "admin", "pw")
	res, err := c.TestContactPoint(context.Background(), "urbex", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Type != "telegram" || res[0].OK || !strings.Contains(res[0].Error, "401") || !res[1].OK {
		t.Errorf("results: %+v", res)
	}
	if strings.Join(tested, ",") != "urbex-telegram,urbex-webhook" {
		t.Errorf("tested %v", tested)
	}
	if _, err := c.TestContactPoint(context.Background(), "nope", "hello"); err == nil || !strings.Contains(err.Error(), "no contact point nope") {
		t.Errorf("missing contact point: %v", err)
	}
	if _, err := New(srv.URL, "admin", "bad").TestContactPoint(context.Background(), "urbex", "x"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad password: %v", err)
	}
}
