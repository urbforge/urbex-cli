// Package netalloc deterministically allocates static IPv4 addresses for
// base-service LXCs out of a configured CIDR, so Terraform can assign
// them and the Ansible inventory can be built without depending on DHCP
// lease discovery.
package netalloc

import (
	"fmt"
	"net"
)

// Allocate returns count sequential IPv4 addresses within cidr, starting
// at host offset (e.g. offset 200 in 192.168.1.0/24 yields 192.168.1.200,
// .201, ...), along with the network's prefix length.
func Allocate(cidr string, offset, count int) ([]string, int, error) {
	if count < 0 {
		return nil, 0, fmt.Errorf("count must be >= 0, got %d", count)
	}

	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, 0, fmt.Errorf("parsing cidr %q: %w", cidr, err)
	}

	base := ipnet.IP.To4()
	if base == nil {
		return nil, 0, fmt.Errorf("cidr %q is not an IPv4 network", cidr)
	}
	prefixLen, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil, 0, fmt.Errorf("cidr %q is not an IPv4 network", cidr)
	}

	addrs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		ip := addOffset(base, offset+i)
		if !ipnet.Contains(ip) {
			return nil, 0, fmt.Errorf("offset %d is outside network %s", offset+i, cidr)
		}
		addrs = append(addrs, ip.String())
	}

	return addrs, prefixLen, nil
}

func addOffset(base net.IP, offset int) net.IP {
	v := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	v += uint32(offset)
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).To4()
}
