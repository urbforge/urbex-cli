package runner

import (
	"context"
	"fmt"
	"io"
)

// TerraformChange runs `terraform apply` or `terraform destroy` (verb) in
// dir, without prompting. Proxmox serializes LXC creation and deletion on
// a storage with a lock that gives up after a few seconds, so Terraform's
// default 10 operations at once - five base services, or LXCs created
// while just-destroyed ones are still being freed - can fail with "can't
// lock file ... got timeout". Fewer at a time, and one more try (both
// commands are idempotent), get through.
func TerraformChange(ctx context.Context, r CommandRunner, out io.Writer, dir, verb string, env []string) error {
	cmd := Command{Dir: dir, Name: "terraform", Args: []string{verb, "-auto-approve", "-parallelism=2"}, Env: env}
	err := r.Run(ctx, cmd)
	if err == nil || ctx.Err() != nil {
		return err
	}
	fmt.Fprintf(out, "terraform %s failed (%v); trying once more - Proxmox storage locks can time out under load.\n", verb, err)
	if err := r.Run(ctx, cmd); err != nil {
		return fmt.Errorf("terraform %s: %w", verb, err)
	}
	return nil
}

// TerraformGrowClones runs `terraform apply` once more after LXCs were
// created as linked clones of urbex's template: Proxmox gives a clone the
// template's disk, and the bpg provider grows it to disk_gb only on an
// update, not when it creates the clone. The second apply resizes the
// disks in place, and changes nothing when they have their size already.
func TerraformGrowClones(ctx context.Context, r CommandRunner, out io.Writer, dir string, env []string) error {
	fmt.Fprintln(out, "Growing the cloned LXCs' disks to their sizes...")
	return TerraformChange(ctx, r, out, dir, "apply", env)
}
