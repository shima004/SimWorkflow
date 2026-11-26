package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"simworkflow/internal/experiment"
)

const DevelopDataCapabilityLabel = "io.simworkflow.develop-data"

const defaultDevelopFile = "config/develop.json"

func developDataPath(root, role string) string {
	// Keep this next to the effective module configuration in the result archive.
	return filepath.Join(root, "modules", role, "develop.json")
}

type developFile struct {
	path     string
	explicit bool
}

func pythonDevelopFile(launcher map[string]any) (developFile, error) {
	adf, _ := launcher["adf"].(map[string]any)
	develop, _ := adf["develop"].(map[string]any)
	if value, exists := develop["filename"]; exists {
		name, ok := value.(string)
		if !ok {
			return developFile{}, errors.New("adf.develop.filename must be a string")
		}
		return developFile{name, true}, nil
	}
	return developFile{path: defaultDevelopFile}, nil
}

func javaDevelopFile(app string) (developFile, error) {
	result := developFile{path: defaultDevelopFile}
	visiting := map[string]bool{}
	var readConfig func(string, bool) error
	readConfig = func(path string, optional bool) error {
		path = filepath.Clean(path)
		if visiting[path] {
			return errors.New("cyclic include in Java launch.cfg")
		}
		data, err := os.ReadFile(path)
		if optional && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read Java launch configuration: %w", err)
		}
		visiting[path] = true
		defer delete(visiting, path)
		for _, line := range strings.Split(string(data), "\n") {
			line, _, _ = strings.Cut(line, "#")
			line = strings.TrimSpace(line)
			if include, ok := strings.CutPrefix(line, "!include"); ok {
				if err := readConfig(resolveAgentPath(filepath.Dir(path), strings.TrimSpace(include)), false); err != nil {
					return err
				}
				continue
			}
			end := strings.IndexAny(line, ":=")
			if end < 0 {
				continue
			}
			switch strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[:end]), "+")) {
			case "adf.develop.filename":
				result = developFile{strings.TrimSpace(line[end+1:]), true}
			case "adf.develop.data":
				if strings.TrimSpace(line[end+1:]) != "" {
					return errors.New("move Java adf.develop.data into the JSON file selected by adf.develop.filename before using experiment developData")
				}
			}
		}
		return nil
	}
	err := readConfig(filepath.Join(app, "config", "launch.cfg"), true)
	return result, err
}

func resolveAgentPath(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

func writeDevelopData(app, output string, base developFile, overrides experiment.DevelopData) error {
	values := make(experiment.DevelopData)
	if base.path != "" {
		data, err := os.ReadFile(resolveAgentPath(app, base.path))
		if err == nil {
			if err := json.Unmarshal(data, &values); err != nil || values == nil {
				return fmt.Errorf("base DevelopData file %q must contain a JSON object", base.path)
			}
		} else if !errors.Is(err, os.ErrNotExist) || base.explicit {
			return fmt.Errorf("read base DevelopData file: %w", err)
		}
	}
	maps.Copy(values, overrides)
	return writeJSON(output, values)
}
