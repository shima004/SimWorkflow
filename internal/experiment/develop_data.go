package experiment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DevelopData keeps numbers intact when a plan travels through JSON.
type DevelopData map[string]json.RawMessage

func (d *DevelopData) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: developData must be a mapping", node.Line)
	}
	values := make(DevelopData, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return fmt.Errorf("line %d: developData keys must be strings", key.Line)
		}
		if _, exists := values[key.Value]; exists {
			return fmt.Errorf("line %d: duplicate developData key %q", key.Line, key.Value)
		}
		decoded, err := decodeDevelopValue(value, true)
		if err != nil {
			return err
		}
		values[key.Value] = decoded
	}
	*d = values
	return nil
}

func decodeDevelopValue(node *yaml.Node, allowList bool) (json.RawMessage, error) {
	switch {
	case node.Kind == yaml.SequenceNode && allowList:
		items := make([]json.RawMessage, len(node.Content))
		for i, child := range node.Content {
			value, err := decodeDevelopValue(child, false)
			if err != nil {
				return nil, err
			}
			items[i] = value
		}
		return json.Marshal(items)
	case node.Kind == yaml.ScalarNode:
		switch node.ShortTag() {
		case "!!str", "!!bool", "!!int", "!!float":
			var value any
			if err := node.Decode(&value); err != nil {
				return nil, err
			}
			if v, ok := value.(float64); ok {
				raw, err := json.Marshal(v)
				if err == nil && !bytes.ContainsAny(raw, ".eE") {
					raw = append(raw, '.', '0')
				}
				return raw, err
			}
			return json.Marshal(value)
		}
	}
	return nil, fmt.Errorf("line %d: developData values must be strings, booleans, finite numbers or flat lists of these values", node.Line)
}

func (d DevelopData) MarshalYAML() (any, error) {
	// JSON scalars and flat lists are valid YAML. Preserve their numeric spelling
	// when exporting an experiment definition to a reproduction archive.
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, key := range sortedKeys(d) {
		var value yaml.Node
		if err := yaml.Unmarshal(d[key], &value); err != nil {
			return nil, err
		}
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value.Content[0])
	}
	return mapping, nil
}

func cloneDevelopData(data DevelopData) DevelopData {
	if len(data) == 0 {
		return nil
	}
	copy := make(DevelopData, len(data))
	for key, value := range data {
		copy[key] = bytes.Clone(value)
	}
	return copy
}

func validateDevelopData(data DevelopData, path string, add func(string, string)) {
	for _, key := range sortedKeys(data) {
		if strings.TrimSpace(key) == "" {
			add(path, "keys must not be blank")
		}
		decoder := json.NewDecoder(bytes.NewReader(data[key]))
		decoder.UseNumber()
		var value any
		if !json.Valid(data[key]) || decoder.Decode(&value) != nil || !validDevelopValue(value, true) {
			add(path+"."+key, "must be a string, boolean, number or flat list of these values")
		}
	}
}

func validDevelopValue(value any, allowList bool) bool {
	switch value := value.(type) {
	case string, bool, json.Number:
		return true
	case []any:
		if allowList {
			for _, item := range value {
				if !validDevelopValue(item, false) {
					return false
				}
			}
			return true
		}
	}
	return false
}
