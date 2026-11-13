// Package cli wires up the urbex command tree.
package cli

import (
	"github.com/spf13/cobra"
)

// Execute runs the urbex root command.
func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "urbex",
		Short: "Urbex automates infrastructure for personal apps",
		Long: `Urbex automates the infrastructure needed to build, deploy, and publish
personal apps to staging and production on your own Proxmox server.

See https://github.com/urbforge/urbex for architecture and design decisions.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newInitCmd())
	root.AddCommand(newBootstrapCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newApplyCmd())
	root.AddCommand(newReleaseCmd())
	root.AddCommand(newDeployCmd())
	root.AddCommand(newPromoteCmd())
	root.AddCommand(newSecretCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newDestroyCmd())
	root.AddCommand(newTeardownCmd())
	root.AddCommand(newUsersCmd())
	root.AddCommand(newDoctorCmd())

	return root
}
