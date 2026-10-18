package manifest

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetServiceVersion writes version as the `version` of the service named
// service in the manifest at path, and reports whether the file changed.
// It edits the value where it stands, so comments and formatting stay as
// they were, and keeps its quotes if it had any. A manifest it can't edit
// that way (a value split over lines, an anchor) is left alone and
// reported.
func SetServiceVersion(path, service, version string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, err
	}
	node := serviceVersionNode(&doc, service)
	if node == nil || node.Kind != yaml.ScalarNode {
		return false, fmt.Errorf("%s: service %q has no version to change", path, service)
	}
	if node.Value == version {
		return false, nil
	}

	lines := strings.Split(string(data), "\n")
	line := lines[node.Line-1]
	start := node.Column - 1
	end := scalarEnd(line, start)
	raw := line[start:end]
	quote := ""
	if raw[0] == '"' || raw[0] == '\'' {
		quote = raw[:1]
	}
	if strings.Trim(raw, `"'`) != node.Value {
		return false, fmt.Errorf("%s: can't change the version of %q in place (line %d): edit it by hand", path, service, node.Line)
	}
	lines[node.Line-1] = line[:start] + quote + version + quote + line[end:]
	return true, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// serviceVersionNode finds the `version` value of the named service.
func serviceVersionNode(doc *yaml.Node, service string) *yaml.Node {
	if len(doc.Content) == 0 {
		return nil
	}
	services := mapValue(doc.Content[0], "services")
	if services == nil {
		return nil
	}
	for _, s := range services.Content {
		if n := mapValue(s, "name"); n != nil && n.Value == service {
			return mapValue(s, "version")
		}
	}
	return nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// scalarEnd is the index just after the scalar that starts at start.
func scalarEnd(line string, start int) int {
	if q := line[start]; q == '"' || q == '\'' {
		if i := strings.IndexByte(line[start+1:], q); i >= 0 {
			return start + 1 + i + 1
		}
		return len(line)
	}
	end := start
	for end < len(line) && !strings.ContainsRune(" \t\r,}]", rune(line[end])) {
		end++
	}
	return end
}
