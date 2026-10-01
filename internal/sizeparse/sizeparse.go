// Package sizeparse parses the Kubernetes-style quantity strings used in
// urbex.yaml's resources section (see docs/reference/manifest.md in
// urbforge/urbex), e.g. "512Mi" or "2Gi", into plain integers Terraform
// variables expect.
package sizeparse

import (
	"fmt"
	"strconv"
	"strings"
)

// MB parses a quantity with a Mi or Gi suffix into megabytes.
func MB(s string) (int, error) {
	switch {
	case strings.HasSuffix(s, "Gi"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "Gi"))
		if err != nil {
			return 0, fmt.Errorf("parsing %q: %w", s, err)
		}
		return n * 1024, nil
	case strings.HasSuffix(s, "Mi"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "Mi"))
		if err != nil {
			return 0, fmt.Errorf("parsing %q: %w", s, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("unsupported size %q: expected a Mi or Gi suffix", s)
	}
}

// GB parses a quantity into whole gigabytes, rounding up, with a floor of
// 1 - used for the LXC module's disk_gb, which Proxmox only accepts as a
// whole number.
func GB(s string) (int, error) {
	mb, err := MB(s)
	if err != nil {
		return 0, err
	}
	gb := (mb + 1023) / 1024
	if gb < 1 {
		gb = 1
	}
	return gb, nil
}
