package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// notImplemented returns a RunE that fails with a pointer to the ADR
// describing the command, until the command gets a real implementation.
func notImplemented(name, adr string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("urbex %s: not implemented yet (see %s in urbforge/urbex)", name, adr)
	}
}

func newDeployCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deploy <env>",
		Short: "Trigger the application deploy via Komodo",
		Args:  cobra.ExactArgs(1),
		RunE:  notImplemented("deploy", "docs/decisions/0004-gitops-gitea-komodo.md"),
	}
}

func newPromoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "promote",
		Short: "Promote the current release from staging to production",
		RunE:  notImplemented("promote", "docs/decisions/0010-promotion-flow.md"),
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [env]",
		Short: "Show the current status of the platform or of a project environment",
		Args:  cobra.MaximumNArgs(1),
		RunE:  notImplemented("status", "docs/architecture.md"),
	}
}

func newDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <env>",
		Short: "Remove an environment's infrastructure",
		Args:  cobra.ExactArgs(1),
		RunE:  notImplemented("destroy", "docs/architecture.md"),
	}
}
