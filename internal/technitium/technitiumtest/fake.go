// Package technitiumtest is an in-memory stand-in for the parts of
// Technitium DNS's HTTP API internal/technitium uses, for tests.
package technitiumtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Server is a fake Technitium DNS server holding zones and their A
// records.
type Server struct {
	*httptest.Server
	mu      sync.Mutex
	Zones   map[string]bool              // by name
	Records map[string]map[string]string // zone -> domain -> ip
	// Token is what CreateToken returns; empty refuses with a bad
	// username/password error.
	Token string
	// Forwarders are the servers set with SetForwarders.
	Forwarders []string
	// DNSSECValidation is the dnssecValidation setting; Technitium's
	// default is true.
	DNSSECValidation bool
}

// NewServer starts a fake Technitium DNS server.
func NewServer() *Server {
	s := &Server{
		Zones:            map[string]bool{},
		Records:          map[string]map[string]string{},
		Token:            "fake-token",
		DNSSECValidation: true,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func ok(w http.ResponseWriter, response any) {
	if response == nil {
		response = map[string]any{}
	}
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "response": response, "server": "fake-technitium"})
}

func fail(w http.ResponseWriter, message string) {
	json.NewEncoder(w).Encode(map[string]any{"status": "error", "errorMessage": message, "server": "fake-technitium"})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()

	switch r.URL.Path {
	case "/api/user/createToken":
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "server": "fake-technitium",
			"username": q.Get("user"), "tokenName": q.Get("tokenName"), "token": s.Token,
		})
	case "/api/zones/create":
		zone := q.Get("zone")
		if s.Zones[zone] {
			fail(w, "Zone already exists: "+zone)
			return
		}
		s.Zones[zone] = true
		s.Records[zone] = map[string]string{}
		ok(w, map[string]any{"domain": zone})
	case "/api/zones/delete":
		zone := q.Get("zone")
		if !s.Zones[zone] {
			fail(w, "No such zone was found: "+zone)
			return
		}
		delete(s.Zones, zone)
		delete(s.Records, zone)
		ok(w, nil)
	case "/api/settings/set":
		s.Forwarders = nil
		if f := q.Get("forwarders"); f != "" {
			s.Forwarders = strings.Split(f, ",")
		}
		if v := q.Get("dnssecValidation"); v != "" {
			s.DNSSECValidation = v == "true"
		}
		ok(w, map[string]any{"forwarders": s.Forwarders})
	case "/api/zones/records/add":
		zone, domain, ip := q.Get("zone"), q.Get("domain"), q.Get("ipAddress")
		if !s.Zones[zone] {
			fail(w, "Zone does not exist: "+zone)
			return
		}
		s.Records[zone][domain] = ip
		ok(w, map[string]any{"addedRecord": map[string]any{"name": domain, "rData": map[string]any{"ipAddress": ip}}})
	case "/api/zones/records/get":
		zone, domain := q.Get("zone"), q.Get("domain")
		if !s.Zones[zone] {
			fail(w, "Zone does not exist: "+zone)
			return
		}
		records := []map[string]any{}
		if ip, exists := s.Records[zone][domain]; exists {
			records = append(records, map[string]any{"name": domain, "type": "A", "rData": map[string]any{"ipAddress": ip}})
		}
		ok(w, map[string]any{"zone": map[string]any{"name": zone}, "records": records})
	case "/api/zones/records/delete":
		zone, domain := q.Get("zone"), q.Get("domain")
		if _, exists := s.Records[zone][domain]; !exists {
			fail(w, "Cannot delete record: no such record exists.")
			return
		}
		delete(s.Records[zone], domain)
		ok(w, nil)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// RecordIP returns the A record's IP for domain in zone, or "" if none.
func (s *Server) RecordIP(zone, domain string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Records[zone][domain]
}

// HasZone reports whether zone exists.
func (s *Server) HasZone(zone string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Zones[zone]
}
