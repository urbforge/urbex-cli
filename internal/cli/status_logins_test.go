package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintLogins_ListsTechnitium(t *testing.T) {
	var out bytes.Buffer
	printLogins(&out, "/Users/me/urbex-work/home/.urbex/credentials.yaml")
	for _, want := range []string{
		"Technitium admin",
		"technitiumAdminPassword",
		"Gitea and Komodo urbex-admin",
		"passwords in /Users/me/urbex-work/home/.urbex/credentials.yaml",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
