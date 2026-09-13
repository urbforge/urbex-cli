// Command urbex is the CLI entry point for Urbex.
package main

import (
	"fmt"
	"os"

	"github.com/urbforge/urbex-cli/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
