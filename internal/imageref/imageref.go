// Package imageref resolves the tag of a published image to its digest,
// for the applications urbex pulls rather than builds: each environment
// runs the digest its tag resolved to, written to the application's
// version.env as VERSION=<tag>@<digest>, until its version is changed.
// See docs/decisions/0028-application-model.md in urbforge/urbex.
//
// Only public images are supported: the registry is asked anonymously,
// as Docker does for a pull without credentials.
package imageref

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Pinned is what an environment runs of an image: a tag and the digest it
// resolved to.
type Pinned struct {
	Tag    string
	Digest string
}

// String is the form version.env holds, and compose appends to the image:
// "17.6@sha256:...".
func (p Pinned) String() string { return p.Tag + "@" + p.Digest }

// ParsePinned reads a version.env VERSION of an image application; ok is
// false if v isn't one (no digest).
func ParsePinned(v string) (Pinned, bool) {
	tag, digest, ok := strings.Cut(v, "@")
	if !ok || tag == "" || !strings.HasPrefix(digest, "sha256:") {
		return Pinned{}, false
	}
	return Pinned{Tag: tag, Digest: digest}, true
}

// Major is a tag's major version: its leading number, after an optional
// "v" ("17.6" and "17" are 17, "v3" is 3, "14-vectorchord0.4.3" is 14);
// ok is false for a tag that doesn't start with one ("latest").
func Major(tag string) (int, bool) {
	t := strings.TrimPrefix(tag, "v")
	end := 0
	for end < len(t) && t[end] >= '0' && t[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(t[:end])
	return n, err == nil
}

// SameMajor reports whether moving from one tag to another keeps the
// major version. Tags without one are the same major only if they are
// the same tag: urbex can't tell what "latest" moved to.
func SameMajor(from, to string) bool {
	a, okA := Major(from)
	b, okB := Major(to)
	if !okA || !okB {
		return from == to
	}
	return a == b
}

// Reference is an image's repository: the registry it is pulled from and
// its path there.
type Reference struct {
	// Registry is the registry's host ("registry-1.docker.io" for Docker
	// Hub).
	Registry string
	// Repository is the image's path in it ("library/postgres").
	Repository string
}

// Parse reads an image name as Docker does: "postgres" is
// docker.io/library/postgres, "valkey/valkey" docker.io/valkey/valkey,
// "ghcr.io/immich-app/postgres" ghcr.io's immich-app/postgres. A tag or
// digest in the name is refused: the manifest's version says it.
func Parse(image string) (Reference, error) {
	if image == "" || strings.ContainsAny(image, "@ ") {
		return Reference{}, fmt.Errorf("invalid image %q", image)
	}
	registry, rest := "docker.io", image
	if first, after, ok := strings.Cut(image, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		registry, rest = first, after
	}
	if strings.Contains(rest, ":") {
		return Reference{}, fmt.Errorf("image %q has a tag: set it as the application's version instead", image)
	}
	if registry == "docker.io" || registry == "index.docker.io" {
		registry = "registry-1.docker.io"
		if !strings.Contains(rest, "/") {
			rest = "library/" + rest
		}
	}
	if rest == "" {
		return Reference{}, fmt.Errorf("invalid image %q", image)
	}
	return Reference{Registry: registry, Repository: rest}, nil
}

// manifestTypes are the manifests a tag can point to; an index (a
// multi-platform image) is preferred, so that its digest is what's pinned
// and each LXC pulls its own platform.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Resolver resolves tags against registries over the OCI distribution
// API.
type Resolver struct {
	// HTTP is the client used; nil means http.DefaultClient.
	HTTP *http.Client
	// Scheme is "https" unless set (tests set "http").
	Scheme string
}

// Resolve returns the digest image:tag points to now.
func (r *Resolver) Resolve(ctx context.Context, image, tag string) (string, error) {
	ref, err := Parse(image)
	if err != nil {
		return "", err
	}
	scheme := r.Scheme
	if scheme == "" {
		scheme = "https"
	}
	manifestURL := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, ref.Registry, ref.Repository, url.PathEscape(tag))

	// HEAD first: registries return the digest in a header, and Docker Hub
	// doesn't count a HEAD against its anonymous pull limit, as it does a
	// GET. GET only when the registry doesn't give the digest that way.
	token := ""
	res, err := r.request(ctx, http.MethodHead, manifestURL, ref.Repository, &token)
	if err != nil {
		return "", fmt.Errorf("%s:%s: %w", image, tag, err)
	}
	res.Body.Close()
	if err := statusError(res, image, tag); err != nil && res.StatusCode != http.StatusMethodNotAllowed {
		return "", err
	}
	if digest := res.Header.Get("Docker-Content-Digest"); res.StatusCode == http.StatusOK && strings.HasPrefix(digest, "sha256:") {
		return digest, nil
	}

	if res, err = r.request(ctx, http.MethodGet, manifestURL, ref.Repository, &token); err != nil {
		return "", fmt.Errorf("%s:%s: %w", image, tag, err)
	}
	defer res.Body.Close()
	if err := statusError(res, image, tag); err != nil {
		return "", err
	}
	if digest := res.Header.Get("Docker-Content-Digest"); strings.HasPrefix(digest, "sha256:") {
		return digest, nil
	}
	// The digest of a manifest is the hash of its bytes.
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("%s:%s: reading the manifest: %w", image, tag, err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// request asks the registry for a manifest with method, getting an
// anonymous pull token into *token the first time the registry wants one.
func (r *Resolver) request(ctx context.Context, method, u, repository string, token *string) (*http.Response, error) {
	res, err := r.do(ctx, method, u, *token)
	if err != nil || res.StatusCode != http.StatusUnauthorized || *token != "" {
		return res, err
	}
	challenge := res.Header.Get("WWW-Authenticate")
	res.Body.Close()
	if *token, err = r.token(ctx, challenge, repository); err != nil {
		return nil, err
	}
	return r.do(ctx, method, u, *token)
}

// statusError is what a registry's answer to a manifest request means,
// nil if it found it.
func statusError(res *http.Response, image, tag string) error {
	switch {
	case res.StatusCode == http.StatusOK:
		return nil
	case res.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s:%s: no such image or tag", image, tag)
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s:%s: the registry refused an anonymous pull (%d) - only public images are supported", image, tag, res.StatusCode)
	default:
		return fmt.Errorf("%s:%s: registry answered %s", image, tag, res.Status)
	}
}

func (r *Resolver) client() *http.Client {
	if r.HTTP == nil {
		return http.DefaultClient
	}
	return r.HTTP
}

func (r *Resolver) do(ctx context.Context, method, u, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestTypes)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := r.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking the registry: %w", err)
	}
	return res, nil
}

// token gets an anonymous pull token from the registry's token service,
// as its 401 challenge says (Bearer realm="...",service="...").
func (r *Resolver) token(ctx context.Context, challenge, repository string) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Errorf("the registry wants %q authentication - only public images are supported", scheme)
	}
	p := parseChallenge(params)
	if p["realm"] == "" {
		return "", fmt.Errorf("the registry's challenge has no realm: %q", challenge)
	}
	u, err := url.Parse(p["realm"])
	if err != nil {
		return "", fmt.Errorf("the registry's token realm %q: %w", p["realm"], err)
	}
	q := u.Query()
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	q.Set("scope", "repository:"+repository+":pull")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	res, err := r.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("getting a pull token: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("getting a pull token: %s", res.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("reading the pull token: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", fmt.Errorf("the registry gave no pull token")
}

// parseChallenge reads the key="value" pairs of a WWW-Authenticate
// challenge.
func parseChallenge(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		s = strings.TrimLeft(s, " ,")
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var value string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				value, rest = rest[1:], ""
			} else {
				value, rest = rest[1:end+1], rest[end+2:]
			}
		} else {
			value, rest, _ = strings.Cut(rest, ",")
		}
		out[strings.ToLower(strings.TrimSpace(key))] = value
		s = rest
	}
	return out
}
