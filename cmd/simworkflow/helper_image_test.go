package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"simworkflow/internal/config"
)

func TestHelperImagePrecedence(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead && r.Method != http.MethodPut {
			t.Errorf("unexpected network request: %s", r.Method)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, yaml, dotenv, environment, flag, want string
		setEnvironment                              bool
	}{
		{name: "bundled default", want: defaultHelperImage},
		{name: "common YAML", yaml: "registry.example.com/helper:yaml", want: "registry.example.com/helper:yaml"},
		{name: "dotenv", yaml: "registry.example.com/helper:yaml", dotenv: "registry.example.com/helper:dotenv", want: "registry.example.com/helper:dotenv"},
		{name: "environment", yaml: "registry.example.com/helper:yaml", dotenv: "registry.example.com/helper:dotenv", environment: "registry.example.com/helper:env", setEnvironment: true, want: "registry.example.com/helper:env"},
		{name: "flag", yaml: "registry.example.com/helper:yaml", dotenv: "registry.example.com/helper:dotenv", environment: "registry.example.com/helper:env", setEnvironment: true, flag: "registry.example.com/helper:flag", want: "registry.example.com/helper:flag"},
		{name: "empty environment resets default", yaml: "registry.example.com/helper:yaml", setEnvironment: true, want: defaultHelperImage},
		{name: "flag overrides invalid configuration", yaml: "https://invalid.example/helper", flag: "registry.example.com/helper:flag", want: "registry.example.com/helper:flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath, envPath, output := filepath.Join(dir, "config.yaml"), filepath.Join(dir, ".env"), filepath.Join(dir, "workflow.yaml")
			if err := os.WriteFile(configPath, []byte(globalTestConfig+"workflow:\n  helperImage: \""+tc.yaml+"\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			env := ""
			if tc.dotenv != "" {
				env = "SIMWORKFLOW_HELPER_IMAGE=" + tc.dotenv + "\n"
			}
			if err := os.WriteFile(envPath, []byte(env), 0600); err != nil {
				t.Fatal(err)
			}
			get := func(key string) (string, bool) {
				if key == "S3_ENDPOINT" {
					return server.URL, true
				}
				if key == "SIMWORKFLOW_HELPER_IMAGE" && tc.setEnvironment {
					return tc.environment, true
				}
				return "", false
			}
			args := []string{"--config", configPath, "--env-file", envPath, "experiment", "render", "-f", filepath.Join(root, "examples/experiments/python-module-comparison.yaml"), "--config-storage", "inline", "--output", output}
			if tc.flag != "" {
				args = append(args, "--helper-image", tc.flag)
			}
			var out, errOut bytes.Buffer
			if err := run(args, get, &out, &errOut); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if count := bytes.Count(data, []byte("image: "+tc.want+"\n")); count != 24 {
				t.Fatalf("all prepare/collector containers must use selected helper: count=%d", count)
			}
		})
	}
}

func TestInvalidHelperImageFailsBeforeNetwork(t *testing.T) {
	for _, image := range []string{"", "https://user:private-secret@registry.example.com/helper:v1", "registry.example.com/helper:v1 -X private-secret", "registry.example.com/helper@sha256:invalid"} {
		var out, errOut bytes.Buffer
		err := runRemote([]string{"render", "-f", "unused.yaml", "--output", "unused-output.yaml", "--helper-image", image}, config.Config{}, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), "helper-image") || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("expected safe image validation error: %v", err)
		}
	}
}

func TestHelperHelpDoesNotEchoInvalidConfiguredURL(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runRemote([]string{"render", "--help"}, config.Config{HelperImage: "https://user:private-secret@registry.example.com/helper:v1"}, &out, &errOut)
	if err != nil || strings.Contains(errOut.String(), "private-secret") || !strings.Contains(errOut.String(), "invalid configured image") {
		t.Fatal("help must sanitize invalid image settings without rejecting a later explicit override")
	}
}
