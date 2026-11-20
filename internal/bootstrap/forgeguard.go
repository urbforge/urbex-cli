package bootstrap

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// forgeComposeCopy is the forge's compose file as its LXC runs it, copied
// into the GitOps repo by every bootstrap (the gitea role), 0.1.0's
// included.
var forgeComposeCopy = filepath.Join(PlatformStacksDir, "gitea", "compose.yaml")

// giteaImage matches the forge image of a platform created by urbex
// 0.1.0: Gitea, which Forgejo replaced (ADR-0034).
var giteaImage = regexp.MustCompile(`(?m)^\s*image:\s*(docker\.io/)?gitea/gitea[:@]\S*`)

// ErrPlatformRunsGitea is returned by Run on a platform whose forge is
// still Gitea: Forgejo can't open the database of Gitea 1.23 and later.
var ErrPlatformRunsGitea = errors.New("the platform's forge is Gitea")

// refuseGiteaPlatform stops a bootstrap that would start Forgejo on
// Gitea's data (urbforge/urbex-cli#116): Forgejo refuses a database
// migrated by Gitea past 1.22 and exits, and the forge stays down.
// It reads only the GitOps repo; nothing has been changed yet.
func refuseGiteaPlatform(gitopsRepoPath string) error {
	data, err := os.ReadFile(filepath.Join(gitopsRepoPath, forgeComposeCopy))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	m := giteaImage.Find(data)
	if m == nil {
		return nil
	}
	return fmt.Errorf(`%w (%s, in %s): this platform was created by urbex 0.1.0.
This urbex runs Forgejo, which can't read the database of Gitea 1.24, so it
won't touch the forge or anything else on this platform. To move to this
release, start the platform again:

  urbex destroy <env>   # in each project, for each environment (teardown requires it);
                        # the applications' named volumes go with their LXCs: back them up first
  urbex teardown        # the platform and its LXCs, Gitea's repositories with them
  urbex bootstrap       # a new platform, on Forgejo

then push your projects' repositories again from your clones and 'urbex apply'
their environments. Or keep using urbex 0.1.0 on this platform.
See "Upgrading" in the CHANGELOG (0.2.0)`, ErrPlatformRunsGitea, strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(string(m), " ")), forgeComposeCopy)
}
