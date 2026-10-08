package bootstrap

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestCheckAddresses(t *testing.T) {
	cfg := validConfig()
	ips, _, err := allocateIPs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	byName := PlatformRecords(cfg, ips)
	matching := func(ctx context.Context, host string) ([]string, error) { return []string{byName[host]}, nil }
	if warning, err := CheckAddresses(context.Background(), cfg, matching); warning != "" || err != nil {
		t.Errorf("matching records: %q, %v", warning, err)
	}

	moved := func(ctx context.Context, host string) ([]string, error) {
		if host == "git.urbex.example.com" {
			return []string{"192.168.1.250"}, nil
		}
		return matching(ctx, host)
	}
	if _, err := CheckAddresses(context.Background(), cfg, moved); err == nil || !strings.Contains(err.Error(), "git.urbex.example.com is 192.168.1.250, but urbex.platform.yaml makes it 192.168.1.200") {
		t.Errorf("a moved service must stop the command, got %v", err)
	}

	down := func(ctx context.Context, host string) ([]string, error) {
		return nil, &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}
	}
	if warning, err := CheckAddresses(context.Background(), cfg, down); err != nil || !strings.Contains(warning, "could not check") {
		t.Errorf("technitium down: %q, %v - want a warning", warning, err)
	}

	missing := func(ctx context.Context, host string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	if warning, err := CheckAddresses(context.Background(), cfg, missing); err != nil || !strings.Contains(warning, "urbex bootstrap") {
		t.Errorf("no records yet: %q, %v - want a warning", warning, err)
	}

	broken := func(ctx context.Context, host string) ([]string, error) { return nil, errors.New("connection refused") }
	if warning, err := CheckAddresses(context.Background(), cfg, broken); err != nil || warning == "" {
		t.Errorf("unreachable: %q, %v - want a warning", warning, err)
	}
}
