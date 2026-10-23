// Package moduleconfig renders ADF module settings from an existing base file.
package moduleconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

var identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*)*$`)

// Render preserves unspecified settings and rejects unknown override keys.
// format is java (ADF module.cfg) or python (ADF module.yaml).
func Render(base []byte, format string, overrides map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(overrides))
	for key, value := range overrides {
		if !identifier.MatchString(key) || !identifier.MatchString(value) {
			return nil, errors.New("module keys and implementations must be dot-separated identifiers")
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	switch format {
	case "java":
		return renderJava(base, overrides, keys)
	case "python":
		return renderPython(base, overrides, keys)
	default:
		return nil, errors.New("module format must be java or python")
	}
}

func renderJava(base []byte, overrides map[string]string, keys []string) ([]byte, error) {
	newline := "\n"
	if bytes.Contains(base, []byte("\r\n")) {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(string(base), "\r\n", "\n"), "\n")
	positions := map[string]int{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid Java module assignment at line %d", i+1)
		}
		if _, exists := positions[key]; exists {
			return nil, fmt.Errorf("duplicate Java module key %s", key)
		}
		positions[key] = i
	}
	for _, key := range keys {
		index, ok := positions[key]
		if !ok {
			return nil, fmt.Errorf("module key %s is absent from the base configuration", key)
		}
		prefix, _, _ := strings.Cut(lines[index], ":")
		lines[index] = prefix + ": " + overrides[key]
	}
	return []byte(strings.Join(lines, newline)), nil
}

func renderPython(base []byte, overrides map[string]string, keys []string) ([]byte, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(base))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode Python module config: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("Python module config must contain exactly one YAML document")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("Python module config must be a YAML mapping")
	}
	leaves := map[string]*yaml.Node{}
	var checkAliases func(*yaml.Node) error
	checkAliases = func(node *yaml.Node) error {
		if node.Kind == yaml.AliasNode || node.Anchor != "" {
			return errors.New("Python module config anchors and aliases are not supported")
		}
		for _, child := range node.Content {
			if err := checkAliases(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := checkAliases(&root); err != nil {
		return nil, err
	}
	var walk func(*yaml.Node, string) error
	walk = func(node *yaml.Node, prefix string) error {
		if node.Kind != yaml.MappingNode {
			if _, exists := leaves[prefix]; exists {
				return fmt.Errorf("ambiguous Python module key %s", prefix)
			}
			leaves[prefix] = node
			return nil
		}
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" || key.Anchor != "" {
				return errors.New("Python module config keys must be nonempty strings; merge keys are not supported")
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate Python module key %s", key.Value)
			}
			seen[key.Value] = true
			path := key.Value
			if prefix != "" {
				path = prefix + "." + key.Value
			}
			if err := walk(node.Content[i+1], path); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root.Content[0], ""); err != nil {
		return nil, err
	}
	for _, key := range keys {
		node, ok := leaves[key]
		if !ok {
			return nil, fmt.Errorf("module key %s is absent from the base configuration", key)
		}
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return nil, fmt.Errorf("module key %s must reference a string implementation", key)
		}
		node.Value = overrides[key]
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
