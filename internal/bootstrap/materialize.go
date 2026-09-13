package bootstrap

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/urbforge/urbex-cli/platform"
)

// materializeAssets writes the embedded terraform/ and ansible/ trees
// into gitopsRepoPath, preserving their relative structure (e.g.
// platform/terraform/base/main.tf -> <gitopsRepoPath>/terraform/base/main.tf).
// Existing files are overwritten - re-running bootstrap always resets the
// materialized assets to whatever this urbex-cli version ships (see
// docs/decisions/0017-embedded-base-infra-assets.md: platform updates
// after that point are a separate, explicit action).
func materializeAssets(gitopsRepoPath string) error {
	if err := materializeFS(gitopsRepoPath, assets.Terraform); err != nil {
		return err
	}
	return materializeFS(gitopsRepoPath, assets.Ansible)
}

func materializeFS(destRoot string, embedded fs.FS) error {
	return fs.WalkDir(embedded, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		data, err := fs.ReadFile(embedded, path)
		if err != nil {
			return err
		}

		dest := filepath.Join(destRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
}
