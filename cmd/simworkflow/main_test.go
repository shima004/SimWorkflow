package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
