package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/urbforge/urbex-cli/internal/manifest"
)

func newInitCmd() *cobra.Command {
	var manifestPath string
	var project string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate or validate the urbex.yaml app manifest",
		Long: `If the manifest file does not exist, urbex init creates a starter
urbex.yaml for the app to be edited (typically by the LLM developing the
app). If it already exists, urbex init instead validates it against the
urbex manifest schema and reports any errors.

See docs/reference/manifest.md in urbforge/urbex.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, manifestPath, project)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "file", "urbex.yaml", "path to the app manifest")
	cmd.Flags().StringVar(&project, "project", "", "project name for a newly scaffolded manifest (defaults to the current directory name)")

	return cmd
}

func runInit(cmd *cobra.Command, manifestPath, project string) error {
	out := cmd.OutOrStdout()

	_, statErr := os.Stat(manifestPath)
	switch {
	case os.IsNotExist(statErr):
		return scaffoldManifest(out, manifestPath, project)
	case statErr != nil:
		return fmt.Errorf("checking %s: %w", manifestPath, statErr)
	}

	return validateManifest(out, manifestPath)
}

func scaffoldManifest(out io.Writer, manifestPath, project string) error {
	if project == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determining project name: %w", err)
		}
		project = filepath.Base(cwd)
	}

	if err := manifest.WriteTemplate(manifestPath, project); err != nil {
		return fmt.Errorf("scaffolding %s: %w", manifestPath, err)
	}

	fmt.Fprintf(out, "Created %s for project %q.\n", manifestPath, project)
	fmt.Fprintln(out, "Declare a frontend and/or services in it, then run 'urbex init' again to validate.")
	return nil
}

func validateManifest(out io.Writer, manifestPath string) error {
	result, err := manifest.ValidateFile(manifestPath)
	if err != nil {
		return fmt.Errorf("validating %s: %w", manifestPath, err)
	}

	if len(result.Errors) == 0 {
		fmt.Fprintf(out, "%s is valid.\n", manifestPath)
		return nil
	}

	fmt.Fprintf(out, "%s failed validation (%d error(s)):\n", manifestPath, len(result.Errors))
	for _, e := range result.Errors {
		fmt.Fprintf(out, "  - %s\n", e)
	}
	return fmt.Errorf("%s failed validation", manifestPath)
}
