// Package materialize writes the embedded base/project Terraform modules
// and Ansible playbooks (see github.com/urbforge/urbex-cli/platform) out
// to a GitOps repo working directory. Shared by both `urbex bootstrap`
// (base services) and `urbex plan`/`apply` (project services). See
// docs/decisions/0017-embedded-base-infra-assets.md in urbforge/urbex.
package materialize

import (
	"io/fs"
	"os"
	"path/filepath"

	platformassets "github.com/urbforge/urbex-cli/platform"
)

// Assets writes the full embedded terraform/ and ansible/ trees into
// gitopsRepoPath, preserving their relative structure. Existing files are
// overwritten - re-running a command that calls this always resets the
// materialized assets to whatever this urbex-cli version ships.
func Assets(gitopsRepoPath string) error {
	if err := writeFS(gitopsRepoPath, platformassets.Terraform); err != nil {
		return err
	}
	return writeFS(gitopsRepoPath, platformassets.Ansible)
}

// ProjectTerraform instantiates the generic per-service Terraform module
// (platform/terraform/project) at
// <gitopsRepoPath>/terraform/projects/<project>/<env>/, the fixed
// location its main.tf's "../../../modules/lxc" module source assumes.
// Used by urbex plan/apply - see docs/decisions/0009-one-lxc-per-service-per-env.md
// in urbforge/urbex.
func ProjectTerraform(gitopsRepoPath, project, env string) error {
	sub, err := fs.Sub(platformassets.Terraform, "terraform/project")
	if err != nil {
		return err
	}
	dest := filepath.Join(gitopsRepoPath, "terraform", "projects", project, env)
	return writeFS(dest, sub)
}

func writeFS(destRoot string, embedded fs.FS) error {
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
