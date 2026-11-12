package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// outputFlag is --output: text (the default), for a person, or json, one
// JSON document for a program - a web UI, an MCP server, the Claude
// Code plugin (ADR-0032).
type outputFlag struct{ value string }

// Register adds --output (-o) to cmd.
func (o *outputFlag) Register(cmd *cobra.Command) {
	cmd.Flags().VarP(o, "output", "o", "text or json")
}

// JSON reports whether --output json was given.
func (o *outputFlag) JSON() bool { return o.value == "json" }

func (o *outputFlag) String() string {
	if o.value == "" {
		return "text"
	}
	return o.value
}

func (o *outputFlag) Set(v string) error {
	if v != "text" && v != "json" {
		return fmt.Errorf("must be text or json")
	}
	o.value = v
	return nil
}

func (o *outputFlag) Type() string { return "format" }

// writeJSON prints v as indented JSON.
func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
