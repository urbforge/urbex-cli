package imageref

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct{ image, registry, repo string }{
		{"postgres", "registry-1.docker.io", "library/postgres"},
		{"valkey/valkey", "registry-1.docker.io", "valkey/valkey"},
		{"docker.io/valkey/valkey", "registry-1.docker.io", "valkey/valkey"},
		{"docker.io/postgres", "registry-1.docker.io", "library/postgres"},
		{"ghcr.io/immich-app/postgres", "ghcr.io", "immich-app/postgres"},
		{"lscr.io/linuxserver/radarr", "lscr.io", "linuxserver/radarr"},
		{"localhost:5000/team/app", "localhost:5000", "team/app"},
	} {
		got, err := Parse(tc.image)
		if err != nil || got.Registry != tc.registry || got.Repository != tc.repo {
			t.Errorf("Parse(%q) = %+v, %v; want %s %s", tc.image, got, err, tc.registry, tc.repo)
		}
	}
	for _, bad := range []string{"", "postgres:17", "ghcr.io/x/y:1", "postgres@sha256:abc", "ghcr.io/"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q): expected an error", bad)
		}
	}
}

func TestMajorAndSameMajor(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want int
		ok   bool
	}{
		{"17", 17, true}, {"17.6", 17, true}, {"v3", 3, true}, {"v3.1.0", 3, true},
		{"14-vectorchord0.4.3-pgvectors0.2.0", 14, true}, {"latest", 0, false}, {"alpine", 0, false},
	} {
		if got, ok := Major(tc.tag); got != tc.want || ok != tc.ok {
			t.Errorf("Major(%q) = %d, %v; want %d, %v", tc.tag, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		from, to string
		same     bool
	}{
		{"17", "17.6", true}, {"17.6", "17.7", true}, {"17", "18", false}, {"v3", "v3.2", true},
		{"latest", "latest", true}, {"latest", "17", false}, {"17", "latest", false},
	} {
		if got := SameMajor(tc.from, tc.to); got != tc.same {
			t.Errorf("SameMajor(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.same)
		}
	}
}

func TestParsePinned(t *testing.T) {
	p, ok := ParsePinned("17.6@sha256:abc")
	if !ok || p.Tag != "17.6" || p.Digest != "sha256:abc" || p.String() != "17.6@sha256:abc" {
		t.Errorf("ParsePinned = %+v, %v", p, ok)
	}
	for _, bad := range []string{"1.4.2", "a1b2c3d", "@sha256:abc", "17@md5:x"} {
		if _, ok := ParsePinned(bad); ok {
			t.Errorf("ParsePinned(%q): expected not pinned", bad)
		}
	}
}

// registry is a registry that, like Docker Hub, wants an anonymous
// Bearer token from its token service.
func registry(t *testing.T, digestHeader bool) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	manifest := []byte(`{"schemaVersion":2}`)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:library/postgres:pull" || r.URL.Query().Get("service") != "registry.test" {
				http.Error(w, "bad scope", http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"token":"t0k"}`))
		case r.Header.Get("Authorization") != "Bearer t0k":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="registry.test",scope="repository:library/postgres:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v2/library/postgres/manifests/17":
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				http.Error(w, "no accept", http.StatusBadRequest)
				return
			}
			if digestHeader {
				w.Header().Set("Docker-Content-Digest", "sha256:fromheader")
			}
			w.Write(manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestResolve_GetsAnAnonymousTokenAndReadsTheDigest(t *testing.T) {
	srv, seen := registry(t, true)
	r := &Resolver{Scheme: "http"}
	host := strings.TrimPrefix(srv.URL, "http://")

	digest, err := r.Resolve(context.Background(), host+"/library/postgres", "17")
	if err != nil || digest != "sha256:fromheader" {
		t.Fatalf("Resolve = %q, %v; want the registry's digest (requests: %v)", digest, err, *seen)
	}

	_, err = r.Resolve(context.Background(), host+"/library/postgres", "99")
	if err == nil || !strings.Contains(err.Error(), "no such image or tag") {
		t.Errorf("expected a missing tag to say so, got %v", err)
	}
}

func TestResolve_HashesTheManifestWithoutADigestHeader(t *testing.T) {
	srv, _ := registry(t, false)
	r := &Resolver{Scheme: "http"}
	digest, err := r.Resolve(context.Background(), strings.TrimPrefix(srv.URL, "http://")+"/library/postgres", "17")
	sum := sha256.Sum256([]byte(`{"schemaVersion":2}`))
	if want := "sha256:" + hex.EncodeToString(sum[:]); err != nil || digest != want {
		t.Errorf("Resolve = %q, %v; want %s", digest, err, want)
	}
}

func TestResolve_PrivateImagesAreRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="private"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	r := &Resolver{Scheme: "http"}
	_, err := r.Resolve(context.Background(), strings.TrimPrefix(srv.URL, "http://")+"/team/app", "1")
	if err == nil || !strings.Contains(err.Error(), "only public images") {
		t.Errorf("expected a private registry to be refused, got %v", err)
	}
}

func TestParseChallenge(t *testing.T) {
	got := parseChallenge(`realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/postgres:pull"`)
	if got["realm"] != "https://auth.docker.io/token" || got["service"] != "registry.docker.io" || got["scope"] != "repository:library/postgres:pull" {
		t.Errorf("parseChallenge = %v", got)
	}
}
