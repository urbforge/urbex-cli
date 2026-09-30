package gitea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateToken_DeletesExistingThenCreatesWithBasicAuth(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		user, pass, ok := r.BasicAuth()
		if !ok || user != "urbex-admin" || pass != "pw" {
			t.Errorf("expected basic auth urbex-admin/pw, got %q/%q (ok=%v)", user, pass, ok)
		}
		switch r.Method + " " + r.URL.Path {
		case "DELETE /api/v1/users/urbex-admin/tokens/urbex-cli":
			w.WriteHeader(http.StatusNotFound)
		case "POST /api/v1/users/urbex-admin/tokens":
			var body struct {
				Name   string   `json:"name"`
				Scopes []string `json:"scopes"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Name != "urbex-cli" || len(body.Scopes) != 1 || body.Scopes[0] != "write:repository" {
				t.Errorf("unexpected token request: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"sha1":"abc123"}`))
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	token, err := New(server.URL, "").CreateToken(context.Background(), "urbex-admin", "pw", "urbex-cli", []string{"write:repository"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if token != "abc123" {
		t.Errorf("token = %q, want abc123", token)
	}
	if len(calls) != 2 {
		t.Errorf("expected delete then create, got %v", calls)
	}
}

func TestEnsureOrg_CreatesOnlyWhenMissing(t *testing.T) {
	exists := false
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token tok" {
			t.Errorf("unexpected Authorization header %q", got)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/orgs/urbex":
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte(`{}`))
		case "POST /api/v1/orgs":
			created++
			exists = true
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "tok")
	for i := 0; i < 2; i++ {
		if err := c.EnsureOrg(context.Background(), "urbex"); err != nil {
			t.Fatalf("EnsureOrg: %v", err)
		}
	}
	if created != 1 {
		t.Errorf("expected exactly one create, got %d", created)
	}
}

func TestEnsureRepo_CreatesPrivateRepoUnderOrg(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/repos/urbex/hello":
			w.WriteHeader(http.StatusNotFound)
		case "POST /api/v1/orgs/urbex/repos":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "hello" || body["private"] != true || body["default_branch"] != "main" {
				t.Errorf("unexpected repo request: %v", body)
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	if err := New(server.URL, "tok").EnsureRepo(context.Background(), "urbex", "hello"); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
}

func TestBranchExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/urbex/hello/branches/staging" {
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := New(server.URL, "tok")
	if ok, err := c.BranchExists(context.Background(), "urbex", "hello", "staging"); err != nil || !ok {
		t.Errorf("expected staging to exist, got ok=%v err=%v", ok, err)
	}
	if ok, err := c.BranchExists(context.Background(), "urbex", "hello", "main"); err != nil || ok {
		t.Errorf("expected main to be missing, got ok=%v err=%v", ok, err)
	}
}

func TestEnsureWebhook_CreatesThenUpdatesByURL(t *testing.T) {
	const target = "http://10.0.0.201:9120/listener/github/procedure/hello-staging/staging"
	var hooks []map[string]any
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/repos/urbex/hello/hooks":
			json.NewEncoder(w).Encode(hooks)
		case "POST /api/v1/repos/urbex/hello/hooks":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			config := body["config"].(map[string]any)
			if body["type"] != "gitea" || body["branch_filter"] != "staging" || config["secret"] != "s3cret" || config["content_type"] != "json" {
				t.Errorf("unexpected webhook body %v", body)
			}
			hooks = append(hooks, map[string]any{"id": 7, "config": map[string]any{"url": config["url"]}})
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		case "PATCH /api/v1/repos/urbex/hello/hooks/7":
			w.Write([]byte(`{}`))
		case "DELETE /api/v1/repos/urbex/hello/hooks/7":
			hooks = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "tok")
	for i := 0; i < 2; i++ {
		if err := c.EnsureWebhook(context.Background(), "urbex", "hello", target, "s3cret", "staging"); err != nil {
			t.Fatalf("EnsureWebhook: %v", err)
		}
	}
	if err := c.DeleteWebhook(context.Background(), "urbex", "hello", target); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if err := c.DeleteWebhook(context.Background(), "urbex", "hello", target); err != nil {
		t.Fatalf("DeleteWebhook (already gone): %v", err)
	}
	want := []string{"GET", "POST", "GET", "PATCH", "GET", "DELETE", "GET"}
	if len(calls) != len(want) {
		t.Fatalf("unexpected calls %v", calls)
	}
	for i, w := range want {
		if calls[i][:len(w)] != w {
			t.Errorf("call %d = %s, want %s", i, calls[i], w)
		}
	}
}

func TestListAndCreateTags(t *testing.T) {
	var created map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/repos/urbex/hello/tags":
			w.Write([]byte(`[{"name":"v0.1.0"},{"name":"v0.1.1"}]`))
		case "POST /api/v1/repos/urbex/hello/tags":
			json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c := New(server.URL, "tok")
	tags, err := c.ListTags(context.Background(), "urbex", "hello")
	if err != nil || len(tags) != 2 || tags[1] != "v0.1.1" {
		t.Fatalf("ListTags = %v, %v", tags, err)
	}
	if err := c.CreateTag(context.Background(), "urbex", "hello", "v0.1.2", "main", "Release 0.1.2"); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	if created["tag_name"] != "v0.1.2" || created["target"] != "main" {
		t.Errorf("unexpected tag request %v", created)
	}
}

func TestContainerImageExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/packages/urbex/container/hello-api/0.1.0":
			w.Write([]byte(`{}`))
		case "/api/v1/packages/urbex/container/hello-api/0.1.1":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := New(server.URL, "tok")
	if ok, err := c.ContainerImageExists(context.Background(), "urbex", "hello-api", "0.1.0"); err != nil || !ok {
		t.Errorf("expected existing image, got ok=%v err=%v", ok, err)
	}
	if ok, err := c.ContainerImageExists(context.Background(), "urbex", "hello-api", "0.1.1"); err != nil || ok {
		t.Errorf("expected missing image, got ok=%v err=%v", ok, err)
	}
	if _, err := c.ContainerImageExists(context.Background(), "urbex", "other", "x"); err == nil {
		t.Error("expected an error on a 500 response")
	}
}
