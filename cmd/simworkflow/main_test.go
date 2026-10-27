package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"simworkflow/internal/config"
	"simworkflow/internal/experiment"
	"simworkflow/internal/reproduction"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Legacy command fixtures test routing/rendering with declared image names.
	// Capture is integration-tested against local registry and S3 APIs separately.
	prepareReproduction = func(ctx context.Context, c config.Config, e *experiment.Experiment, helper, version, commit string) (*experiment.Experiment, reproduction.Metadata, error) {
		return e, reproduction.Metadata{Schema: reproduction.Schema, Platform: "linux/amd64", HelperImage: helper, CLIVersion: version, CLICommit: commit, Definition: e}, nil
	}
	// Never read a developer's real common configuration during CLI tests.
	dir, err := os.MkdirTemp("", "simworkflow-test-user-config-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create isolated test config directory")
		os.Exit(1)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const globalTestConfig = `argoWorkflow:
  server: https://argo.example.com
  token: private-token
s3:
  endpoint: https://s3.example.com
  bucket: results
  accessKeyID: private-key
  secretAccessKey: private-secret
registry:
  url: https://ghcr.io
httpTimeout: 13s
`

func TestVersionWithoutConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("broken private-value"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := run([]string{"--config", "missing.yaml", "--version"}, func(string) (string, bool) { t.Fatal("version must not load credentials"); return "", false }, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != fmt.Sprintf("simworkflow %s (commit %s)\n", version, commit) || errOut.Len() != 0 {
		t.Fatal("unexpected version output")
	}
}

func TestCommonYAMLWorksFromAnyDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, "simworkflow", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(globalTestConfig), 0600); err != nil {
		t.Fatal(err)
	}
	empty := func(string) (string, bool) { return "", false }
	for _, tc := range []struct {
		name, env string
		args      []string
		process   map[string]string
		want      string
	}{
		{name: "first directory", want: "13s"},
		{name: "another directory", want: "13s"},
		{name: "dotenv overrides YAML", env: "HTTP_TIMEOUT=17s\n", want: "17s"},
		{name: "process overrides both", env: "HTTP_TIMEOUT=17s\n", process: map[string]string{"HTTP_TIMEOUT": "19s"}, want: "19s"},
		{name: "empty token overrides YAML", process: map[string]string{"ARGO_WORKFLOW_TOKEN": ""}, want: "Argo authentication: anonymous"},
		{name: "disable dotenv only", env: "malformed private-value\n", args: []string{"--env-file="}, want: "13s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tc.env != "" {
				if err := os.WriteFile(".env", []byte(tc.env), 0600); err != nil {
					t.Fatal(err)
				}
			}
			get := empty
			if tc.process != nil {
				get = func(key string) (string, bool) { v, ok := tc.process[key]; return v, ok }
			}
			var out, errOut bytes.Buffer
			if err := run(append(tc.args, "config", "check"), get, &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String()+errOut.String(), "private-") {
				t.Fatal("wrong configuration precedence or leaked credentials")
			}
		})
	}
}

func TestConfigInitPathAndExplicitSelection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Chdir(t.TempDir())
	// These commands must work before configuration and despite a broken .env.
	if err := os.WriteFile(".env", []byte("broken private-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	empty := func(string) (string, bool) { return "", false }
	var out, errOut bytes.Buffer
	if err := run([]string{"config", "path"}, empty, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "simworkflow", "config.yaml")
	if strings.TrimSpace(out.String()) != path {
		t.Fatal("wrong default path")
	}
	out.Reset()
	if err := run([]string{"config", "init"}, empty, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"config", "init"}, empty, &out, &errOut); !os.IsExist(err) {
		t.Fatal("init must not replace existing configuration")
	}
	other := filepath.Join(t.TempDir(), "custom.yaml")
	if err := os.WriteFile(other, []byte(strings.ReplaceAll(globalTestConfig, "13s", "23s")), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--env-file=", "--config", other, "config", "check"}, empty, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "23s") {
		t.Fatal("explicit config selection was not used")
	}
	if err := run([]string{"--env-file=", "--config", other + ".missing", "config", "check"}, empty, &out, &errOut); !os.IsNotExist(err) {
		t.Fatal("explicit missing YAML must fail")
	}
	if err := run([]string{"--env-file=", "--config=", "config", "check"}, empty, &out, &errOut); err == nil {
		t.Fatal("disabling YAML must remove its settings")
	}
}

func TestMalformedCommonConfigCanBeDisabledAndDoesNotAffectOfflineCommands(t *testing.T) {
	experimentFile, err := filepath.Abs("../../examples/experiments/precompute-baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Chdir(t.TempDir())
	path := filepath.Join(root, "simworkflow", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("private-secret: not-a-known-field\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"ARGO_WORKFLOW_SERVER": "https://argo.example.com", "S3_ENDPOINT": "https://s3.example.com", "S3_BUCKET": "results", "S3_ACCESS_KEY_ID": "key", "S3_SECRET_ACCESS_KEY": "secret", "REGISTRY_URL": "https://ghcr.io"}
	get := func(key string) (string, bool) { v, ok := values[key]; return v, ok }
	var out, errOut bytes.Buffer
	if err := run([]string{"config", "check"}, get, &out, &errOut); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("malformed common config must fail without exposing its contents")
	}
	if err := run([]string{"--config=", "--env-file=", "config", "check"}, get, &out, &errOut); err != nil {
		t.Fatal("environment-only operation must remain supported")
	}
	if err := run([]string{"experiment", "validate", "-f", experimentFile}, get, &out, &errOut); err != nil {
		t.Fatal("offline commands must not load connection configuration")
	}
}

func TestConfigCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "ARGO_WORKFLOW_SERVER=https://argo.example.com\nARGO_WORKFLOW_TOKEN=secret-token\nS3_ENDPOINT=http://localhost:9000\nS3_BUCKET=workflows\nS3_ACCESS_KEY_ID=secret-key\nS3_SECRET_ACCESS_KEY=secret-password\nREGISTRY_URL=https://registry.example.com\nREGISTRY_TOKEN=secret-registry-token\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	lookup := func(string) (string, bool) { return "", false }
	if err := run([]string{"-env-file", path, "config", "check"}, lookup, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Configuration valid") || strings.Contains(out.String()+errOut.String(), "secret-") {
		t.Fatal("expected successful check without credentials in output")
	}
	if err := run([]string{"-env-file", path + ".missing", "config", "check"}, lookup, &out, &errOut); err == nil {
		t.Fatal("explicit missing file must fail")
	}
}

func TestAutomaticEnvFile(t *testing.T) {
	values := map[string]string{
		"ARGO_WORKFLOW_SERVER": "https://argo.example.com",
		"S3_ENDPOINT":          "https://s3.example.com", "S3_BUCKET": "workflows",
		"S3_ACCESS_KEY_ID": "test-key", "S3_SECRET_ACCESS_KEY": "test-secret",
		"REGISTRY_URL": "https://registry.example.com",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	for _, tc := range []struct {
		name, content string
		args          []string
		wantError     bool
	}{
		{name: "missing default file is optional"},
		{name: "process environment overrides file", content: "S3_ENDPOINT=invalid\n"},
		{name: "malformed default file fails", content: "not-an-assignment\n", wantError: true},
		{name: "empty option disables loading", content: "not-an-assignment\n", args: []string{"-env-file="}},
		{name: "explicit missing default file fails", args: []string{"-env-file", ".env"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tc.content != "" {
				if err := os.WriteFile(".env", []byte(tc.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, errOut bytes.Buffer
			err := run(append(tc.args, "config", "check"), lookup, &out, &errOut)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v; wantError = %v", err, tc.wantError)
			}
		})
	}
	t.Run("default file supplies configuration", func(t *testing.T) {
		t.Chdir(t.TempDir())
		var content strings.Builder
		for key, value := range values {
			fmt.Fprintf(&content, "%s=%s\n", key, value)
		}
		if err := os.WriteFile(".env", []byte(content.String()), 0600); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if err := run([]string{"config", "check"}, func(string) (string, bool) { return "", false }, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Configuration valid") {
			t.Fatal("expected configuration from default .env")
		}
	})
}
