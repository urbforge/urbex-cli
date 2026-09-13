package netalloc

import (
	"reflect"
	"testing"
)

func TestAllocate(t *testing.T) {
	addrs, prefix, err := Allocate("192.168.1.0/24", 200, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{
		"192.168.1.200",
		"192.168.1.201",
		"192.168.1.202",
		"192.168.1.203",
		"192.168.1.204",
	}
	if !reflect.DeepEqual(addrs, want) {
		t.Errorf("addrs = %v, want %v", addrs, want)
	}
	if prefix != 24 {
		t.Errorf("prefix = %d, want 24", prefix)
	}
}

func TestAllocate_OffsetOverflowsNetwork(t *testing.T) {
	_, _, err := Allocate("192.168.1.0/24", 250, 10)
	if err == nil {
		t.Fatal("expected an error when the allocation overflows the network")
	}
}

func TestAllocate_InvalidCIDR(t *testing.T) {
	_, _, err := Allocate("not-a-cidr", 0, 1)
	if err == nil {
		t.Fatal("expected an error for an invalid CIDR")
	}
}

func TestAllocate_RejectsIPv6(t *testing.T) {
	_, _, err := Allocate("2001:db8::/32", 0, 1)
	if err == nil {
		t.Fatal("expected an error for an IPv6 CIDR")
	}
}

func TestAllocate_ZeroCount(t *testing.T) {
	addrs, _, err := Allocate("192.168.1.0/24", 200, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 0 {
		t.Errorf("expected no addresses, got %v", addrs)
	}
}
