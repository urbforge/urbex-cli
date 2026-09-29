package komodo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginAndCreateAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login/LoginLocalUser":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["username"] != "admin" || body["password"] != "pw" {
				t.Errorf("unexpected login body %v", body)
			}
			w.Write([]byte(`{"type":"Jwt","data":{"jwt":"the-jwt"}}`))
		case "/auth/manage/CreateApiKey":
			if got := r.Header.Get("Authorization"); got != "the-jwt" {
				t.Errorf("expected jwt in Authorization, got %q", got)
			}
			w.Write([]byte(`{"key":"K_x_K","secret":"S_y_S"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "", "")
	jwt, err := c.Login(context.Background(), "admin", "pw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	key, secret, err := c.CreateAPIKey(context.Background(), jwt, "urbex")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if key != "K_x_K" || secret != "S_y_S" {
		t.Errorf("got key=%q secret=%q", key, secret)
	}
}

func TestEnsureGitProviderAccount_UpdatesExisting(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.Header.Get("X-Api-Key") != "k" || r.Header.Get("X-Api-Secret") != "s" {
			t.Errorf("missing api key headers")
		}
		switch r.URL.Path {
		case "/read/ListGitProviderAccounts":
			w.Write([]byte(`[{"_id":{"$oid":"abc"},"domain":"10.0.0.200:3000","username":"urbex-admin"}]`))
		case "/write/UpdateGitProviderAccount":
			var body struct {
				ID      string         `json:"id"`
				Account map[string]any `json:"account"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.ID != "abc" || body.Account["token"] != "tok" || body.Account["https"] != false {
				t.Errorf("unexpected update body %+v", body)
			}
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	err := New(server.URL, "k", "s").EnsureGitProviderAccount(context.Background(), "10.0.0.200:3000", false, "urbex-admin", "tok")
	if err != nil {
		t.Fatalf("EnsureGitProviderAccount: %v", err)
	}
	if strings.Join(calls, ",") != "/read/ListGitProviderAccounts,/write/UpdateGitProviderAccount" {
		t.Errorf("unexpected calls %v", calls)
	}
}

func TestEnsureBuild_CreatesWhenMissing(t *testing.T) {
	var created map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/read/ListBuilds":
			w.Write([]byte(`[{"name":"other"}]`))
		case "/write/CreateBuild":
			json.NewDecoder(r.Body).Decode(&created)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := BuildConfig{Repo: "urbex/hello", Commit: "abc", DockerfilePath: "Dockerfile.urbex"}
	if err := New(server.URL, "k", "s").EnsureBuild(context.Background(), "hello-api", cfg); err != nil {
		t.Fatalf("EnsureBuild: %v", err)
	}
	config, _ := created["config"].(map[string]any)
	if created["name"] != "hello-api" || config["repo"] != "urbex/hello" || config["dockerfile_path"] != "Dockerfile.urbex" {
		t.Errorf("unexpected create body %v", created)
	}
}

func TestRunBuildAndWaitUpdate(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/execute/RunBuild":
			w.Write([]byte(`{"_id":{"$oid":"u1"},"status":"InProgress"}`))
		case "/read/GetUpdate":
			polls++
			if polls < 2 {
				w.Write([]byte(`{"_id":{"$oid":"u1"},"status":"InProgress"}`))
				return
			}
			w.Write([]byte(`{"_id":{"$oid":"u1"},"status":"Complete","success":false,"commit_hash":"500f12b",
				"logs":[{"stage":"Clone Repo","success":true},{"stage":"Docker Build","success":false,"stderr":"boom"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "k", "s")
	started, err := c.RunBuild(context.Background(), "hello-api")
	if err != nil {
		t.Fatalf("RunBuild: %v", err)
	}
	done, err := c.WaitUpdate(context.Background(), started.ID.OID, time.Millisecond)
	if err != nil {
		t.Fatalf("WaitUpdate: %v", err)
	}
	if done.Success || done.CommitHash != "500f12b" {
		t.Errorf("unexpected update %+v", done)
	}
	if got := done.FailureSummary(); !strings.Contains(got, `"Docker Build"`) || !strings.Contains(got, "boom") {
		t.Errorf("unexpected failure summary %q", got)
	}
}

func TestAPIErrorIsSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Did not find any Build matching nope","trace":[]}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "k", "s").RunBuild(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "Did not find any Build matching nope") {
		t.Errorf("expected the komodo error message, got %v", err)
	}
}
