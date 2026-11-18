package provision

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/urbforge/urbex-cli/internal/tfstate"
)

// stateHostnames are the hostnames of the machines in the environment's
// Terraform state (module.service["<hostname>"], module.vm["<hostname>"]):
// what exists, whether urbex.yaml still declares it or not.
func stateHostnames(tfDir string) (map[string]bool, error) {
	found := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(tfDir, "terraform.tfstate"))
	if os.IsNotExist(err) {
		return found, nil
	}
	if err != nil {
		return nil, err
	}
	containers, err := tfstate.Containers(data)
	if err != nil {
		return nil, err
	}
	for _, c := range containers {
		if h := hostnameOf(c.Address); h != "" {
			found[h] = true
		}
	}
	return found, nil
}

// hostnameOf is the key of a resource address: module.service["x"].... → x.
func hostnameOf(address string) string {
	i := strings.Index(address, `["`)
	if i < 0 {
		return ""
	}
	rest := address[i+2:]
	j := strings.Index(rest, `"]`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// freeGone removes from the allocation ledger the machines that were in
// the environment's Terraform state (before) and no longer are (after),
// and that urbex.yaml doesn't declare (keep): an application removed from
// urbex.yaml, deleted by apply (urbforge/urbex-cli#115). Their addresses
// are free again; urbex teardown would otherwise take the entries for a
// live environment.
func (o *Orchestrator) freeGone(opts Options, before, after, keep map[string]bool) error {
	var gone []string
	for h := range before {
		if !after[h] && !keep[h] {
			gone = append(gone, h)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	sort.Strings(gone)
	ledgerPath := filepath.Join(opts.GitOpsRepoPath, "state", "allocations.json")
	ledger, err := o.loadLedger(opts)
	if err != nil {
		return err
	}
	var freed []string
	for _, h := range gone {
		if _, ok := ledger.Lookup(h); ok {
			ledger.Remove(h)
			freed = append(freed, h)
		}
	}
	if len(freed) == 0 {
		return nil
	}
	if err := ledger.Save(ledgerPath); err != nil {
		return fmt.Errorf("saving allocation ledger: %w", err)
	}
	fmt.Fprintf(o.Out, "Freed the addresses of %s: no longer in urbex.yaml, and deleted.\n", strings.Join(freed, ", "))
	return nil
}
