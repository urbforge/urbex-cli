// Package cloudflaretest is an in-memory stand-in for the parts of
// Cloudflare's API that internal/cloudflare uses, for tests.
package cloudflaretest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Server is a fake Cloudflare API holding one account's tunnels, Access
// objects and one zone's DNS records.
type Server struct {
	*httptest.Server
	mu        sync.Mutex
	nextID    int
	Tunnels   map[string]*Tunnel // by id
	Records   map[string]*Record // by id
	Policies  map[string]map[string]any
	Apps      map[string]map[string]any
	IdPs      []string          // types
	Workers   map[string]string // name -> custom domain hostname
	Forbidden map[string]bool   // path prefixes (after /accounts/<id> or /zones/<id>) answered with 403
}

// Tunnel is a fake tunnel and its ingress.
type Tunnel struct {
	ID, Name string
	Ingress  []map[string]any
	Deleted  bool
}

// Record is a fake DNS record.
type Record struct {
	ID, Type, Name, Content, Comment string
	Proxied                          bool
}

// NewServer starts a fake Cloudflare API.
func NewServer() *Server {
	s := &Server{
		Tunnels:   map[string]*Tunnel{},
		Records:   map[string]*Record{},
		Policies:  map[string]map[string]any{},
		Apps:      map[string]map[string]any{},
		Workers:   map[string]string{},
		Forbidden: map[string]bool{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *Server) id() string {
	s.nextID++
	return fmt.Sprintf("id%04d", s.nextID)
}

func reply(w http.ResponseWriter, status int, result any) {
	w.WriteHeader(status)
	ok := status < 300
	body := map[string]any{"success": ok, "result": result, "errors": []any{}}
	if !ok {
		body["errors"] = []any{map[string]any{"code": 10000, "message": "Authentication error"}}
		body["result"] = nil
	}
	json.NewEncoder(w).Encode(body)
}

// RoutesOf returns a tunnel's hostname -> service, without the catch-all.
func (s *Server) RoutesOf(tunnelID string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	if t := s.Tunnels[tunnelID]; t != nil {
		for _, r := range t.Ingress {
			if h, _ := r["hostname"].(string); h != "" {
				svc, _ := r["service"].(string)
				if p, _ := r["path"].(string); p != "" {
					svc += " " + p
				}
				out[h] = svc
			}
		}
	}
	return out
}

// IngressOf returns a tunnel's rules in order, without the catch-all, as
// "hostname path -> service" ("hostname -> service" without a path).
func (s *Server) IngressOf(tunnelID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	if t := s.Tunnels[tunnelID]; t != nil {
		for _, r := range t.Ingress {
			h, _ := r["hostname"].(string)
			if h == "" {
				continue
			}
			svc, _ := r["service"].(string)
			if p, _ := r["path"].(string); p != "" {
				h += " " + p
			}
			out = append(out, h+" -> "+svc)
		}
	}
	return out
}

// RecordNames returns the names of the DNS records.
func (s *Server) RecordNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for _, r := range s.Records {
		names = append(names, r.Name)
	}
	return names
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // accounts|zones, id, ...
	if len(parts) < 3 {
		reply(w, 404, nil)
		return
	}
	rest := parts[2:]
	if s.Forbidden[rest[0]] || (len(rest) > 1 && s.Forbidden[rest[0]+"/"+rest[1]]) {
		reply(w, 403, nil)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	key := strings.Join(rest, "/")
	switch {
	// tunnels
	case key == "cfd_tunnel" && r.Method == http.MethodGet:
		var out []map[string]any
		for _, t := range s.Tunnels {
			if !t.Deleted && (r.URL.Query().Get("name") == "" || t.Name == r.URL.Query().Get("name")) {
				out = append(out, map[string]any{"id": t.ID, "name": t.Name})
			}
		}
		reply(w, 200, out)
	case key == "cfd_tunnel" && r.Method == http.MethodPost:
		t := &Tunnel{ID: s.id(), Name: body["name"].(string)}
		s.Tunnels[t.ID] = t
		reply(w, 200, map[string]any{"id": t.ID, "name": t.Name})
	case rest[0] == "cfd_tunnel" && len(rest) == 3 && rest[2] == "token":
		reply(w, 200, "token-of-"+rest[1])
	case rest[0] == "cfd_tunnel" && len(rest) == 3 && rest[2] == "configurations":
		t := s.Tunnels[rest[1]]
		if t == nil {
			reply(w, 404, nil)
			return
		}
		if r.Method == http.MethodPut {
			cfg := body["config"].(map[string]any)
			t.Ingress = nil
			for _, i := range cfg["ingress"].([]any) {
				t.Ingress = append(t.Ingress, i.(map[string]any))
			}
		}
		reply(w, 200, map[string]any{"config": map[string]any{"ingress": t.Ingress}})
	case rest[0] == "cfd_tunnel" && len(rest) == 3 && rest[2] == "connections":
		reply(w, 200, nil)
	case rest[0] == "cfd_tunnel" && len(rest) == 2 && r.Method == http.MethodDelete:
		if t := s.Tunnels[rest[1]]; t != nil {
			t.Deleted = true
		}
		reply(w, 200, nil)
	// DNS
	case key == "dns_records" && r.Method == http.MethodGet:
		var out []map[string]any
		for _, rec := range s.Records {
			if rec.Name == r.URL.Query().Get("name") {
				out = append(out, map[string]any{"id": rec.ID, "type": rec.Type, "name": rec.Name, "content": rec.Content, "proxied": rec.Proxied, "comment": rec.Comment})
			}
		}
		reply(w, 200, out)
	case key == "dns_records" && r.Method == http.MethodPost, rest[0] == "dns_records" && r.Method == http.MethodPut:
		rec := &Record{ID: s.id()}
		if r.Method == http.MethodPut {
			rec.ID = rest[1]
		}
		rec.Type, _ = body["type"].(string)
		rec.Name, _ = body["name"].(string)
		rec.Content, _ = body["content"].(string)
		rec.Comment, _ = body["comment"].(string)
		rec.Proxied, _ = body["proxied"].(bool)
		s.Records[rec.ID] = rec
		reply(w, 200, map[string]any{"id": rec.ID})
	case rest[0] == "dns_records" && r.Method == http.MethodDelete:
		delete(s.Records, rest[1])
		reply(w, 200, nil)
	// Access
	case rest[0] == "access" && len(rest) >= 2 && (rest[1] == "policies" || rest[1] == "apps"):
		store := s.Policies
		if rest[1] == "apps" {
			store = s.Apps
		}
		switch {
		case len(rest) == 2 && r.Method == http.MethodGet:
			var out []map[string]any
			for _, v := range store {
				out = append(out, v)
			}
			reply(w, 200, out)
		case len(rest) == 2 && r.Method == http.MethodPost:
			body["id"] = s.id()
			store[body["id"].(string)] = body
			reply(w, 200, body)
		case len(rest) == 3 && r.Method == http.MethodPut:
			body["id"] = rest[2]
			store[rest[2]] = body
			reply(w, 200, body)
		case len(rest) == 3 && r.Method == http.MethodDelete:
			delete(store, rest[2])
			reply(w, 200, nil)
		}
	case key == "access/identity_providers" && r.Method == http.MethodGet:
		var out []map[string]any
		for _, t := range s.IdPs {
			out = append(out, map[string]any{"type": t})
		}
		reply(w, 200, out)
	case key == "access/identity_providers" && r.Method == http.MethodPost:
		s.IdPs = append(s.IdPs, body["type"].(string))
		reply(w, 200, body)
	// Workers
	case key == "workers/domains" && r.Method == http.MethodGet:
		var out []map[string]any
		for name, host := range s.Workers {
			if host == r.URL.Query().Get("hostname") {
				out = append(out, map[string]any{"id": "dom-" + name, "hostname": host, "service": name})
			}
		}
		reply(w, 200, out)
	case rest[0] == "workers" && len(rest) == 3 && rest[1] == "domains" && r.Method == http.MethodDelete:
		s.Workers[strings.TrimPrefix(rest[2], "dom-")] = ""
		reply(w, 200, nil)
	case key == "workers/scripts" && r.Method == http.MethodGet:
		var out []map[string]any
		for name := range s.Workers {
			out = append(out, map[string]any{"id": name})
		}
		reply(w, 200, out)
	case rest[0] == "workers" && len(rest) == 3 && rest[1] == "scripts" && r.Method == http.MethodDelete:
		delete(s.Workers, rest[2])
		reply(w, 200, nil)
	default:
		reply(w, 404, nil)
	}
}
