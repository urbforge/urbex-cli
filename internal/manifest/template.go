package manifest

import (
	"fmt"
	"os"
)

const templateFmt = `apiVersion: urbex/v1
project: %s

# Declare a frontend and/or one or more services below, then run
# 'urbex init' again to validate. See:
# https://github.com/urbforge/urbex/blob/main/docs/reference/manifest.md
#
# frontend:
#   type: web                    # web | mobile
#   provider: cloudflare-pages   # cloudflare-pages (web) | firebase (mobile)
#   buildCommand: npm run build
#   outputDir: dist
#
# services:
#   - name: api
#     runtime: go                # java | python | go | docker
#     port: 8080
`

// WriteTemplate writes a starter manifest for the given project name to
// path. The template is intentionally minimal (no frontend/services
// declared) so it fails validation until edited - it exists to be filled
// in, not to already be a valid manifest.
func WriteTemplate(path, project string) error {
	content := fmt.Sprintf(templateFmt, project)
	return os.WriteFile(path, []byte(content), 0o644)
}
