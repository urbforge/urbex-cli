// Package keycloak is a minimal client for Keycloak's admin REST API: the
// realms, clients and roles Urbex keeps for each project. See
// docs/decisions/0014-keycloak-realm-per-project.md in urbforge/urbex.
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client talks to a Keycloak as its master realm's admin.
type Client struct {
	baseURL, username, password string
	http                        *http.Client
	token                       string
}

// New returns a Client for the Keycloak at baseURL (e.g.
// http://192.168.1.203:8080), logging in as the master realm's admin.
func New(baseURL, username, password string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), username: username, password: password,
		http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/realms/master/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("logging in to keycloak: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("logging in to keycloak as %s: %s %s", c.username, resp.Status, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return err
	}
	c.token = tok.AccessToken
	return nil
}

// errNotFound is a 404 from the admin API.
var errNotFound = fmt.Errorf("not found")

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if c.token == "" {
		if err := c.login(ctx); err != nil {
			return err
		}
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/admin/realms"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling keycloak: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("keycloak %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// EnsureRealm creates the realm if it doesn't exist.
func (c *Client) EnsureRealm(ctx context.Context, realm, displayName string) error {
	err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(realm), nil, nil)
	if err == nil {
		return nil
	}
	if err != errNotFound {
		return err
	}
	body := map[string]any{"realm": realm, "displayName": displayName, "enabled": true}
	if err := c.do(ctx, http.MethodPost, "", body, nil); err != nil {
		return fmt.Errorf("creating realm %s: %w", realm, err)
	}
	return nil
}

// RealmExists reports whether the realm exists.
func (c *Client) RealmExists(ctx context.Context, realm string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(realm), nil, nil)
	if err == errNotFound {
		return false, nil
	}
	return err == nil, err
}

// DeleteRealm deletes the realm, with its users and clients, if it exists.
func (c *Client) DeleteRealm(ctx context.Context, realm string) error {
	err := c.do(ctx, http.MethodDelete, "/"+url.PathEscape(realm), nil, nil)
	if err == errNotFound {
		return nil
	}
	return err
}

// EnsureRoles creates the realm roles that don't exist yet.
func (c *Client) EnsureRoles(ctx context.Context, realm string, roles []string) error {
	for _, role := range roles {
		err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(realm)+"/roles/"+url.PathEscape(role), nil, nil)
		if err == nil {
			continue
		}
		if err != errNotFound {
			return err
		}
		if err := c.do(ctx, http.MethodPost, "/"+url.PathEscape(realm)+"/roles", map[string]string{"name": role}, nil); err != nil {
			return fmt.Errorf("creating role %s in realm %s: %w", role, realm, err)
		}
	}
	return nil
}

// PublicClient is an OIDC client for a frontend: public (no secret),
// authorization code flow with PKCE.
type PublicClient struct {
	ClientID     string
	Name         string
	RedirectURIs []string
	WebOrigins   []string
	// DirectAccessGrants allows logging in with a password straight to
	// the token endpoint (for tests and command-line tools only).
	DirectAccessGrants bool
}

type clientRep struct {
	ID                        string            `json:"id,omitempty"`
	ClientID                  string            `json:"clientId"`
	Name                      string            `json:"name,omitempty"`
	Enabled                   bool              `json:"enabled"`
	PublicClient              bool              `json:"publicClient"`
	StandardFlowEnabled       bool              `json:"standardFlowEnabled"`
	DirectAccessGrantsEnabled bool              `json:"directAccessGrantsEnabled"`
	RedirectURIs              []string          `json:"redirectUris"`
	WebOrigins                []string          `json:"webOrigins"`
	Attributes                map[string]string `json:"attributes"`
}

func (c *Client) findClient(ctx context.Context, realm, clientID string) (*clientRep, error) {
	var list []clientRep
	if err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(realm)+"/clients?clientId="+url.QueryEscape(clientID), nil, &list); err != nil {
		return nil, err
	}
	for _, cl := range list {
		if cl.ClientID == clientID {
			return &cl, nil
		}
	}
	return nil, nil
}

// EnsurePublicClient creates the client, or adds to an existing one the
// redirect URIs and web origins it lacks - the ones already there, added
// by Urbex for another environment or by hand, are kept.
func (c *Client) EnsurePublicClient(ctx context.Context, realm string, pc PublicClient) error {
	existing, err := c.findClient(ctx, realm, pc.ClientID)
	if err != nil {
		return err
	}
	rep := clientRep{
		ClientID: pc.ClientID, Name: pc.Name, Enabled: true, PublicClient: true,
		StandardFlowEnabled: true, DirectAccessGrantsEnabled: pc.DirectAccessGrants,
		RedirectURIs: pc.RedirectURIs, WebOrigins: pc.WebOrigins,
		Attributes: map[string]string{"pkce.code.challenge.method": "S256"},
	}
	if existing == nil {
		if err := c.do(ctx, http.MethodPost, "/"+url.PathEscape(realm)+"/clients", rep, nil); err != nil {
			return fmt.Errorf("creating client %s in realm %s: %w", pc.ClientID, realm, err)
		}
		return nil
	}
	rep.ID = existing.ID
	rep.RedirectURIs = union(existing.RedirectURIs, pc.RedirectURIs)
	rep.WebOrigins = union(existing.WebOrigins, pc.WebOrigins)
	if err := c.do(ctx, http.MethodPut, "/"+url.PathEscape(realm)+"/clients/"+existing.ID, rep, nil); err != nil {
		return fmt.Errorf("updating client %s in realm %s: %w", pc.ClientID, realm, err)
	}
	return nil
}

// RemoveFromClient takes redirect URIs and web origins off a client (an
// environment that's gone); a missing client or realm is not an error.
func (c *Client) RemoveFromClient(ctx context.Context, realm, clientID string, redirectURIs, webOrigins []string) error {
	existing, err := c.findClient(ctx, realm, clientID)
	if err == errNotFound || (err == nil && existing == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	existing.RedirectURIs = minus(existing.RedirectURIs, redirectURIs)
	existing.WebOrigins = minus(existing.WebOrigins, webOrigins)
	return c.do(ctx, http.MethodPut, "/"+url.PathEscape(realm)+"/clients/"+existing.ID, existing, nil)
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func minus(a, b []string) []string {
	drop := map[string]bool{}
	for _, s := range b {
		drop[s] = true
	}
	out := []string{}
	for _, s := range a {
		if !drop[s] {
			out = append(out, s)
		}
	}
	return out
}

// TestUser is a user made for trying a project out: enabled, its profile
// complete (so password logins work), with a permanent password and realm
// roles.
type TestUser struct {
	Username, Password, Email, FirstName, LastName string
	Roles                                          []string
}

type userRep struct {
	ID              string   `json:"id,omitempty"`
	Username        string   `json:"username"`
	Email           string   `json:"email,omitempty"`
	FirstName       string   `json:"firstName,omitempty"`
	LastName        string   `json:"lastName,omitempty"`
	Enabled         bool     `json:"enabled"`
	EmailVerified   bool     `json:"emailVerified"`
	RequiredActions []string `json:"requiredActions"`
}

func (c *Client) findUser(ctx context.Context, realm, username string) (*userRep, error) {
	var list []userRep
	path := "/" + url.PathEscape(realm) + "/users?exact=true&username=" + url.QueryEscape(username)
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	for _, u := range list {
		if u.Username == username {
			return &u, nil
		}
	}
	return nil, nil
}

// EnsureUser creates the user if missing, and in any case sets its
// password and adds its realm roles.
func (c *Client) EnsureUser(ctx context.Context, realm string, u TestUser) error {
	base := "/" + url.PathEscape(realm) + "/users"
	existing, err := c.findUser(ctx, realm, u.Username)
	if err != nil {
		return err
	}
	if existing == nil {
		rep := userRep{Username: u.Username, Email: u.Email, FirstName: u.FirstName, LastName: u.LastName,
			Enabled: true, EmailVerified: true, RequiredActions: []string{}}
		if err := c.do(ctx, http.MethodPost, base, rep, nil); err != nil {
			return fmt.Errorf("creating user %s in realm %s: %w", u.Username, realm, err)
		}
		if existing, err = c.findUser(ctx, realm, u.Username); err != nil || existing == nil {
			return fmt.Errorf("finding the new user %s: %v", u.Username, err)
		}
	}
	cred := map[string]any{"type": "password", "value": u.Password, "temporary": false}
	if err := c.do(ctx, http.MethodPut, base+"/"+existing.ID+"/reset-password", cred, nil); err != nil {
		return fmt.Errorf("setting the password of %s: %w", u.Username, err)
	}
	var roles []map[string]any
	for _, name := range u.Roles {
		var role map[string]any
		if err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(realm)+"/roles/"+url.PathEscape(name), nil, &role); err != nil {
			return fmt.Errorf("reading role %s: %w", name, err)
		}
		roles = append(roles, map[string]any{"id": role["id"], "name": role["name"]})
	}
	if len(roles) > 0 {
		if err := c.do(ctx, http.MethodPost, base+"/"+existing.ID+"/role-mappings/realm", roles, nil); err != nil {
			return fmt.Errorf("giving %s its roles: %w", u.Username, err)
		}
	}
	return nil
}

// Token logs a user in with a client that allows password logins and
// returns the access token - for tests and checks.
func (c *Client) Token(ctx context.Context, realm, clientID, username, password string) (string, error) {
	form := url.Values{"grant_type": {"password"}, "client_id": {clientID}, "username": {username}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/realms/"+url.PathEscape(realm)+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("logging in %s to realm %s: %s %s", username, realm, resp.Status, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&tok)
	return tok.AccessToken, err
}
