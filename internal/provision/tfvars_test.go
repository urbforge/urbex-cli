package provision

import (
	"testing"

	"github.com/urbforge/urbex-cli/internal/manifest"
)

func TestStartupOrder_EachLXCAfterWhatItUses(t *testing.T) {
	m := &manifest.Manifest{Services: []manifest.Service{
		{Name: "db", Provides: manifest.InterfacePostgres, ReadyWithin: "10m"},
		{Name: "api", Port: 80, Healthcheck: "/", ReadyWithin: "90s", Uses: []string{"db"}},
		{Name: "web", Port: 80, Uses: []string{"api", "db"}},
		{Name: "alone", Port: 80},
	}}
	want := map[string][2]int{
		"db":    {10, 300}, // ready_within capped
		"api":   {11, 90},
		"web":   {12, 0}, // nothing uses it: nothing to hold back
		"alone": {10, 0},
	}
	for _, svc := range m.Services {
		order, delay := startupOrder(m, svc)
		if got := [2]int{order, delay}; got != want[svc.Name] {
			t.Errorf("startupOrder(%s) = %v, want %v", svc.Name, got, want[svc.Name])
		}
	}
}
