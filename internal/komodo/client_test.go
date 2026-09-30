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

	cfg := BuildConfig{Repo: "urbex/hello", Branch: "staging", DockerfilePath: "Dockerfile.urbex"}
	if err := New(server.URL, "k", "s").EnsureBuild(context.Background(), "hello-api", cfg); err != nil {
		t.Fatalf("EnsureBuild: %v", err)
	}
	config, _ := created["config"].(map[string]any)
	if created["name"] != "hello-api" || config["repo"] != "urbex/hello" || config["dockerfile_path"] != "Dockerfile.urbex" {
		t.Errorf("unexpected create body %v", created)
	}
}

func TestRunProcedureWaitAndExplainFailure(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/execute/RunProcedure":
			w.Write([]byte(`{"_id":{"$oid":"p1"},"status":"InProgress"}`))
		case "/read/GetUpdate":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["id"] == "aaaaaaaaaaaaaaaaaaaaaaaa" {
				w.Write([]byte(`{"status":"Complete","success":false,
					"logs":[{"stage":"Docker Run","success":false,"stderr":"<span>unknown flag</span>"}]}`))
				return
			}
			polls++
			if polls < 2 {
				w.Write([]byte(`{"_id":{"$oid":"p1"},"status":"InProgress"}`))
				return
			}
			w.Write([]byte(`{"_id":{"$oid":"p1"},"status":"Complete","success":false,
				"logs":[{"stage":"Execute procedure","success":true},
				{"stage":"execution error","success":false,"stdout":"ERROR: see update '<b>aaaaaaaaaaaaaaaaaaaaaaaa</b>'"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "k", "s")
	started, err := c.RunProcedure(context.Background(), "hello-staging")
	if err != nil {
		t.Fatalf("RunProcedure: %v", err)
	}
	done, err := c.WaitUpdate(context.Background(), started.ID.OID, time.Millisecond)
	if err != nil {
		t.Fatalf("WaitUpdate: %v", err)
	}
	if done.Success {
		t.Fatalf("expected a failed update, got %+v", done)
	}
	got := c.ExplainFailure(context.Background(), done)
	if !strings.Contains(got, `"Docker Run"`) || !strings.Contains(got, "unknown flag") || strings.Contains(got, "<span>") {
		t.Errorf("expected the nested update's failure without HTML, got %q", got)
	}
}

func TestAPIErrorIsSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"Did not find any Procedure matching nope","trace":[]}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "k", "s").RunProcedure(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "Did not find any Procedure matching nope") {
		t.Errorf("expected the komodo error message, got %v", err)
	}
}

func TestEnsureDeployment_UpdatesExistingByName(t *testing.T) {
	var updated map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/read/ListDeployments":
			w.Write([]byte(`[{"name":"hello-api-staging","info":{"state":"running","image":"r/o/hello-api:0.0.4-staging"}}]`))
		case "/write/UpdateDeployment":
			json.NewDecoder(r.Body).Decode(&updated)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "k", "s")
	cfg := DeploymentConfig{ServerID: "hello-api-staging", Image: BuildImage("hello-api-staging"), Ports: "8080:8080"}
	if err := c.EnsureDeployment(context.Background(), "hello-api-staging", cfg); err != nil {
		t.Fatalf("EnsureDeployment: %v", err)
	}
	config, _ := updated["config"].(map[string]any)
	image, _ := config["image"].(map[string]any)
	if updated["id"] != "hello-api-staging" || image["type"] != "Build" || config["ports"] != "8080:8080" {
		t.Errorf("unexpected update body %v", updated)
	}

	statuses, err := c.ListDeployments(context.Background())
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if st := statuses["hello-api-staging"]; st.State != "running" || st.Image != "r/o/hello-api:0.0.4-staging" {
		t.Errorf("unexpected status %+v", st)
	}
}

func TestDeleteResource_SkipsMissing(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/read/ListProcedures":
			w.Write([]byte(`[{"name":"hello-staging"}]`))
		case "/write/DeleteProcedure":
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "k", "s")
	if err := c.DeleteResource(context.Background(), "Procedure", "hello-staging"); err != nil {
		t.Fatalf("DeleteResource: %v", err)
	}
	if err := c.DeleteResource(context.Background(), "Procedure", "gone"); err != nil {
		t.Fatalf("DeleteResource(missing): %v", err)
	}
	if strings.Join(calls, ",") != "/read/ListProcedures,/write/DeleteProcedure,/read/ListProcedures" {
		t.Errorf("unexpected calls %v", calls)
	}
}

func TestWaitServers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"name":"a","info":{"state":"Ok"}},{"name":"b","info":{"state":"NotOk"}}]`))
	}))
	defer server.Close()

	c := New(server.URL, "k", "s")
	c.pollInterval = time.Millisecond
	if _, err := c.WaitServers(context.Background(), []string{"a"}, time.Second); err != nil {
		t.Errorf("expected connected server a to satisfy the wait: %v", err)
	}
	_, err := c.WaitServers(context.Background(), []string{"a", "b", "c"}, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "[b c]") {
		t.Errorf("expected b and c reported as not connected, got %v", err)
	}
}

func TestWaitServers_AcceptsTheNewKeyOfARecreatedServer(t *testing.T) {
	rekeys := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/read/ListServers":
			if rekeys == 0 {
				w.Write([]byte(`[{"name":"a","info":{"state":"NotOk","attempted_public_key":"NEWKEY"}},
					{"name":"other","info":{"state":"NotOk","attempted_public_key":"X"}}]`))
				return
			}
			w.Write([]byte(`[{"name":"a","info":{"state":"Ok"}}]`))
		case "/write/UpdateServerPublicKey":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["server"] != "a" || body["public_key"] != "NEWKEY" {
				t.Errorf("unexpected re-key request %v (servers urbex didn't ask about must be left alone)", body)
			}
			rekeys++
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "k", "s")
	c.pollInterval = time.Millisecond
	rekeyed, err := c.WaitServers(context.Background(), []string{"a"}, time.Second)
	if err != nil {
		t.Fatalf("WaitServers: %v", err)
	}
	if rekeys != 1 || strings.Join(rekeyed, ",") != "a" {
		t.Errorf("expected server a to be re-keyed once, got rekeys=%d rekeyed=%v", rekeys, rekeyed)
	}
}

func TestNewStage(t *testing.T) {
	st := NewStage("Build", "RunBuild", "build", []string{"a", "b"})
	if len(st.Executions) != 2 || st.Executions[1].Execution.Params["build"] != "b" || !st.Executions[0].Enabled || !st.Enabled {
		t.Errorf("unexpected stage %+v", st)
	}
}
