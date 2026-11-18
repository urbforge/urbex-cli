package imageref

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	nginx := []string{"latest", "1.28", "1.29", "1.30", "1.30.1", "1.31-alpine", "1.31", "2.0", "2.1", "v1.40"}
	postgres := []string{"latest", "17", "18", "17.6", "17.7", "18.1", "17-alpine", "18-alpine"}
	for _, tc := range []struct {
		current string
		tags    []string
		want    Updates
	}{
		{"1.29", nginx, Updates{Minor: "1.31", Major: "2.1"}},
		{"1.31", nginx, Updates{Major: "2.1"}},
		{"2.1", nginx, Updates{}},
		{"1.31-alpine", nginx, Updates{}},
		{"latest", nginx, Updates{}},
		{"v1.39", nginx, Updates{Minor: "v1.40"}},
		{"17", postgres, Updates{Major: "18"}},
		{"17.6", postgres, Updates{Minor: "17.7", Major: "18.1"}},
		{"17-alpine", postgres, Updates{Major: "18-alpine"}},
	} {
		if got := Newer(tc.current, tc.tags); got != tc.want {
			t.Errorf("Newer(%q) = %+v, want %+v", tc.current, got, tc.want)
		}
	}
}

// Tags follows the registry's pages, with an anonymous token.
func TestTags_FollowsPages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			fmt.Fprint(w, `{"token":"anon"}`)
		case r.Header.Get("Authorization") != "Bearer anon":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v2/library/nginx/tags/list" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/library/nginx/tags/list?n=1000&last=1.28>; rel="next"`)
			fmt.Fprint(w, `{"name":"library/nginx","tags":["1.27","1.28"]}`)
		case r.URL.Path == "/v2/library/nginx/tags/list":
			fmt.Fprint(w, `{"name":"library/nginx","tags":["1.29"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := &Resolver{Scheme: "http"}
	host := strings.TrimPrefix(srv.URL, "http://")
	tags, err := r.Tags(context.Background(), host+"/library/nginx")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, " ") != "1.27 1.28 1.29" {
		t.Errorf("tags = %v", tags)
	}
}
