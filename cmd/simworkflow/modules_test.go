package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateModuleFiles(t *testing.T) {
	for _, tc := range []struct {
		name, experiment, agent, base string
		count                         int
	}{
		{"java", "module-comparison", "comparison", "java.cfg", 3},
		{"python", "python-module-comparison", "python-comparison", "python.yaml", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "generated")
			args := []string{"experiment", "modules", "-f", "../../examples/experiments/" + tc.experiment + ".yaml", "--agent", tc.agent, "--base", "../../examples/module-configs/" + tc.base, "--format", tc.name, "--output-dir", dir}
			var out, errOut bytes.Buffer
			noEnv := func(string) (string, bool) { return "", false }
			if err := run(args, noEnv, &out, &errOut); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest moduleManifest
			if err := json.Unmarshal(contents, &manifest); err != nil {
				t.Fatal(err)
			}
			if len(manifest.Files) != tc.count || manifest.Format != tc.name {
				t.Fatalf("unexpected manifest: %+v", manifest)
			}
			for _, file := range manifest.Files {
				contents, err := os.ReadFile(filepath.Join(dir, file.File))
				if err != nil || !strings.Contains(string(contents), file.Configuration.Modules["SampleRoadDetector.PathPlanning"]) {
					t.Fatalf("expected rendered configuration, got %v", err)
				}
			}
			if err := run(args, noEnv, &out, &errOut); err == nil {
				t.Fatal("existing output directory must not be overwritten")
			}
			if after, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err != nil || !bytes.Equal(after, contents) {
				t.Fatal("existing manifest must remain unchanged")
			}
		})
	}
}

func TestModuleFailureCreatesNoOutput(t *testing.T) {
	for _, tc := range []struct{ name, agent, base string }{
		{"unknown-agent", "missing", "SampleRoadDetector.PathPlanning : team.Default\n"},
		{"missing-key", "comparison", "DifferentKey : team.Default\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			base := filepath.Join(root, "base.cfg")
			if err := os.WriteFile(base, []byte(tc.base), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "generated")
			args := []string{"experiment", "modules", "-f", "../../examples/experiments/module-comparison.yaml", "--agent", tc.agent, "--base", base, "--format", "java", "--output-dir", destination}
			var out, errOut bytes.Buffer
			if err := run(args, func(string) (string, bool) { return "", false }, &out, &errOut); err == nil {
				t.Fatal("expected error")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("failed generation must not create an output directory")
			}
		})
	}
}
