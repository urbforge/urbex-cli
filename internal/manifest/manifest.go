// Package manifest loads, validates, and scaffolds urbex.yaml app manifests
// against the urbex manifest JSON Schema.
package manifest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"
)

// schemaJSON is a vendored copy of docs/schemas/urbex.schema.json from
// urbforge/urbex. Keep the two in sync when the schema changes.
//
//go:embed schema/urbex.schema.json
var schemaJSON []byte

const schemaResourceID = "urbex-manifest.json"

// ValidationResult holds the outcome of validating a manifest file.
type ValidationResult struct {
	Errors []string
}

// ValidateFile reads the YAML manifest at path and validates it against the
// urbex manifest schema.
func ValidateFile(path string) (*ValidationResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return &ValidationResult{Errors: []string{fmt.Sprintf("invalid YAML: %s", err)}}, nil
	}

	instance, err := toJSONCompatible(raw)
	if err != nil {
		return nil, fmt.Errorf("normalizing manifest: %w", err)
	}

	schema, err := compileSchema()
	if err != nil {
		return nil, err
	}

	if err := schema.Validate(instance); err != nil {
		if verr, ok := err.(*jsonschema.ValidationError); ok {
			return &ValidationResult{Errors: flattenValidationError(verr)}, nil
		}
		return &ValidationResult{Errors: []string{err.Error()}}, nil
	}

	return &ValidationResult{}, nil
}

func compileSchema() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaResourceID, bytes.NewReader(schemaJSON)); err != nil {
		return nil, fmt.Errorf("loading embedded manifest schema: %w", err)
	}
	return compiler.Compile(schemaResourceID)
}

// toJSONCompatible round-trips a value decoded by yaml.v3 through JSON so it
// only contains types the jsonschema validator understands (map[string]any,
// []any, string, float64, bool, nil).
func toJSONCompatible(v interface{}) (interface{}, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func flattenValidationError(root *jsonschema.ValidationError) []string {
	var messages []string

	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := e.InstanceLocation
			if loc == "" {
				loc = "/"
			}
			messages = append(messages, fmt.Sprintf("%s: %s", loc, e.Message))
			return
		}
		for _, cause := range e.Causes {
			walk(cause)
		}
	}
	walk(root)

	return messages
}
