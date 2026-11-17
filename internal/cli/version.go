package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/version"
)

func newVersionCmd() *cobra.Command {
	var output outputFlag
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print urbex's version, commit and build date",
		Long: `Prints the version of this urbex binary, the commit it was built from and
when, and the platform: say it in a bug report. A release says its
version (0.1.0); a build from source says "dev" and its commit. --output
json prints it as one JSON document. urbex --version prints the same line.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			i := version.Get()
			if output.JSON() {
				return writeJSON(cmd.OutOrStdout(), i)
			}
			fmt.Fprintln(cmd.OutOrStdout(), i.String())
			return nil
		},
	}
	output.Register(cmd)
	return cmd
}
