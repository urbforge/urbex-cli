package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/cloudflare"
	"github.com/urbforge/urbex-cli/internal/credentials"
	"github.com/urbforge/urbex-cli/internal/discover"
	"github.com/urbforge/urbex-cli/internal/platform"
	"github.com/urbforge/urbex-cli/internal/proxmoxapi"
)

func newDiscoverCmd() *cobra.Command {
	var proxmoxURL, gitopsRepoPath string
	var noScan, write bool
	var output outputFlag

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Find what urbex.platform.yaml needs on Proxmox, the LAN and Cloudflare, and propose it",
		Long: `Reads, with the stored tokens and nothing else:

- the Proxmox node (--proxmox https://<host>:8006, or proxmox.apiUrl of an
  existing urbex.platform.yaml): its version, nodes, the token's pool and
  bridge, the storages for LXC and VM disks, the Debian 13 LXC templates,
  whether the platform's VMIDs are free, an Urbex platform already in the
  pool;
- the LAN, from the bridge's address on the node, and a block of 20 free
  addresses for the platform, scanned by ping and TCP (--no-scan skips the
  scan);
- with a Cloudflare token, its zones.

Then it proposes a complete urbex.platform.yaml, each value with where it
comes from; --write saves it in the GitOps repo, never over an existing
one. It changes nothing on Proxmox or Cloudflare and prints no secret.
--output json prints what it found and the proposal as one JSON document.
It exits non-zero when it couldn't propose a config. See
docs/reference/cli.md in urbforge/urbex.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, out := cmd.Context(), cmd.OutOrStdout()
			creds, err := credentials.Load()
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
			repoPath, repoErr := resolveGitOpsRepoPath(gitopsRepoPath)
			configPath := ""
			if repoErr == nil {
				configPath = filepath.Join(repoPath, "urbex.platform.yaml")
			}
			if proxmoxURL == "" && configPath != "" {
				if cfg, err := platform.LoadFile(configPath); err == nil {
					proxmoxURL = cfg.Proxmox.APIURL
				}
			}
			if proxmoxURL == "" {
				return fmt.Errorf("where is Proxmox? pass --proxmox https://<host>:8006")
			}
			if creds.ProxmoxToken == "" {
				return fmt.Errorf("no Proxmox token: store it with 'urbex-op set proxmoxToken' (or URBEX_PROXMOX_TOKEN) - see docs/reference/credentials.md#proxmox-api-token")
			}

			in := discover.Input{APIURL: proxmoxURL, TokenID: strings.SplitN(creds.ProxmoxToken, "=", 2)[0]}
			in.Insecure, in.Proxmox = proxmoxClient(ctx, discover.NormalizeAPIURL(proxmoxURL), creds.ProxmoxToken)
			if creds.CloudflareToken != "" {
				in.Cloudflare = cloudflare.New(cloudflare.DefaultBaseURL, creds.CloudflareToken, "", "")
			}
			if !noScan {
				in.Probe = discover.Probe
			}
			if home, err := os.UserHomeDir(); err == nil {
				if key, err := os.ReadFile(filepath.Join(home, ".ssh", "urbex.pub")); err == nil {
					in.SSHPublicKey = string(key)
				}
			}
			r := discover.Run(ctx, in)

			written := ""
			if write && r.Proposal != "" {
				if repoErr != nil {
					return repoErr
				}
				if _, err := os.Stat(configPath); err == nil {
					return fmt.Errorf("%s exists: discover never overwrites it - compare it with the proposal ('urbex discover' without --write)", configPath)
				}
				if err := os.WriteFile(configPath, []byte(r.Proposal), 0o644); err != nil {
					return err
				}
				written = configPath
			}
			if output.JSON() {
				if err := writeJSON(out, r); err != nil {
					return err
				}
			} else {
				printDiscover(out, r, written)
			}
			if len(r.Errors) > 0 {
				return fmt.Errorf("no config proposed: %s", strings.Join(r.Errors, "; "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&proxmoxURL, "proxmox", "", "Proxmox's address, https://<host>:8006 (default: proxmox.apiUrl of urbex.platform.yaml)")
	cmd.Flags().StringVar(&gitopsRepoPath, "gitops-repo", "", "local working directory of the GitOps repo (or set URBEX_GITOPS_REPO)")
	cmd.Flags().BoolVar(&noScan, "no-scan", false, "don't scan the LAN for free addresses")
	cmd.Flags().BoolVar(&write, "write", false, "write the proposal to urbex.platform.yaml in the GitOps repo, if there is none")
	output.Register(cmd)
	return cmd
}

// proxmoxClient is a client for the node: one that checks its certificate
// when the certificate is trusted, otherwise one that doesn't - Proxmox's
// own is self-signed. It reports which.
func proxmoxClient(ctx context.Context, apiURL, token string) (bool, *proxmoxapi.Client) {
	c := proxmoxapi.New(apiURL, token, false)
	_, err := c.Version(ctx)
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	if err != nil && (errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) || strings.Contains(err.Error(), "certificate")) {
		return true, proxmoxapi.New(apiURL, token, true)
	}
	return false, c
}

func printDiscover(out io.Writer, r *discover.Result, written string) {
	p, n := r.Proxmox, r.Network
	if p.Version != "" {
		fmt.Fprintf(out, "Proxmox VE %s at %s, node %s", p.Version, p.APIURL, p.Node)
		if p.Pool != "" {
			fmt.Fprintf(out, ", pool %s", p.Pool)
		}
		fmt.Fprintln(out)
		var storages []string
		for _, s := range p.Storages {
			if s.Has("rootdir") && s.Has("images") {
				storages = append(storages, fmt.Sprintf("%s (%s, %d GB free)", s.Name, s.Type, s.AvailGB))
			}
		}
		fmt.Fprintf(out, "  storages for disks: %s\n", strings.Join(storages, ", "))
		fmt.Fprintf(out, "  Debian 13 templates: %s\n", strings.Join(p.LXCTemplates, ", "))
		if len(p.PoolMembers) > 0 {
			fmt.Fprintf(out, "  machines in the pool: %d\n", len(p.PoolMembers))
		}
	}
	if n.CIDR != "" {
		fmt.Fprintf(out, "LAN %s, gateway %s", n.CIDR, n.Gateway)
		if n.Scanned {
			fmt.Fprintf(out, ", %d addresses answer", len(n.InUse))
		}
		fmt.Fprintln(out)
	}
	if cf := r.Cloudflare; cf != nil {
		var zones []string
		for _, z := range cf.Zones {
			zones = append(zones, z.Name)
		}
		fmt.Fprintf(out, "Cloudflare: token active %v, zones %s\n", cf.TokenActive, strings.Join(zones, ", "))
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	for _, e := range r.Errors {
		fmt.Fprintf(out, "error: %s\n", e)
	}
	if r.Proposal == "" {
		return
	}
	if written != "" {
		fmt.Fprintf(out, "\nWrote %s:\n\n", written)
	} else {
		fmt.Fprintf(out, "\nProposed urbex.platform.yaml (save it with --write):\n\n")
	}
	fmt.Fprint(out, r.Proposal)
}
