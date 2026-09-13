package manifest

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadTyped validates the manifest at path (see ValidateFile) and, only
// if it's valid, also decodes it into a typed Manifest for programmatic
// use by urbex plan/apply. If validation fails, the returned *Manifest is
// nil and result.Errors is non-empty; err is reserved for I/O/YAML
// parsing failures that ValidateFile itself would also have reported.
func LoadTyped(path string) (*Manifest, *ValidationResult, error) {
	result, err := ValidateFile(path)
	if err != nil {
		return nil, nil, err
	}
	if len(result.Errors) > 0 {
		return nil, result, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, nil, fmt.Errorf("parsing %s after it passed schema validation: %w", path, err)
	}

	return &m, result, nil
}
