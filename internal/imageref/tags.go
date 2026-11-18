package imageref

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Tags lists image's tags, as the registry's tags/list gives them,
// following its pages (urbforge/urbex-cli#113).
func (r *Resolver) Tags(ctx context.Context, image string) ([]string, error) {
	ref, err := Parse(image)
	if err != nil {
		return nil, err
	}
	scheme := r.Scheme
	if scheme == "" {
		scheme = "https"
	}
	base := fmt.Sprintf("%s://%s", scheme, ref.Registry)
	next := fmt.Sprintf("%s/v2/%s/tags/list?n=1000", base, ref.Repository)
	token := ""
	var tags []string
	for page := 0; next != "" && page < 50; page++ {
		res, err := r.request(ctx, http.MethodGet, next, ref.Repository, &token)
		if err != nil {
			return nil, fmt.Errorf("%s: listing tags: %w", image, err)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: listing tags: %w", image, err)
		}
		if err := statusError(res, image, "tags"); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &body); err != nil {
			return nil, fmt.Errorf("%s: listing tags: %w", image, err)
		}
		tags = append(tags, body.Tags...)
		next = nextPage(res.Header.Get("Link"), base)
	}
	return tags, nil
}

// nextPage is the URL of a Link header's rel="next", made absolute.
func nextPage(link, base string) string {
	for _, part := range strings.Split(link, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		target = strings.Trim(strings.TrimSpace(target), "<>")
		if u, err := url.Parse(target); err == nil && !u.IsAbs() {
			return base + target
		}
		return target
	}
	return ""
}

// Updates are the newer versions of a tag among an image's tags: Minor
// is the newest of the same major (minor and patch versions), Major the
// newest of a higher major; empty when there is none.
type Updates struct {
	Minor string `json:"minor,omitempty"`
	Major string `json:"major,omitempty"`
}

// tagShape splits a version tag: an optional "v", its numbers, and what
// follows them ("17.6-alpine": "", [17 6], "-alpine").
var tagShape = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(.*)$`)

type version struct {
	prefix, suffix string
	parts          []int
}

func parseVersion(tag string) (version, bool) {
	m := tagShape.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	var parts []int
	for _, p := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		parts = append(parts, n)
	}
	return version{prefix: m[1], suffix: m[3], parts: parts}, true
}

func (a version) less(b version) bool {
	for i := range a.parts {
		if a.parts[i] != b.parts[i] {
			return a.parts[i] < b.parts[i]
		}
	}
	return false
}

// Newer finds the updates of current among tags. Only tags of the same
// shape count: as many numbers, the same "v" and the same suffix, so that
// "1.29" is compared with "1.30" and "2.0", not with "1.30.1", "latest" or
// "1.30-alpine" - the tag stays as precise as the one urbex.yaml asks for.
// A tag without a version ("latest") has no updates.
func Newer(current string, tags []string) Updates {
	cur, ok := parseVersion(current)
	if !ok {
		return Updates{}
	}
	var u Updates
	var minor, major version
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok || v.prefix != cur.prefix || v.suffix != cur.suffix || len(v.parts) != len(cur.parts) || !cur.less(v) {
			continue
		}
		if v.parts[0] == cur.parts[0] {
			if u.Minor == "" || minor.less(v) {
				u.Minor, minor = t, v
			}
		} else if u.Major == "" || major.less(v) {
			u.Major, major = t, v
		}
	}
	return u
}
