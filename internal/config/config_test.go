package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{
		"ARGO_WORKFLOW_SERVER": "https://argo.example.com", "S3_ENDPOINT": "http://localhost:9000",
		"S3_BUCKET": "workflows", "S3_ACCESS_KEY_ID": "test-key", "S3_SECRET_ACCESS_KEY": "secret-value",
		"REGISTRY_URL": "https://registry.example.com",
	}
}

func lookup(values map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := values[k]; return v, ok }
}

func TestLoad(t *testing.T) {
	c, err := Load(lookup(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Argo.Namespace != "argo" || c.S3.Region != "us-east-1" || !c.S3.ForcePathStyle || c.HTTPTimeout != 30*time.Second || c.SimScopeURL != "" {
		t.Fatal("unexpected defaults")
	}
	for _, tc := range []struct{ key, value string }{
		{"CREDENTIALS_STORE", "invalid"},
		{"ARGO_WORKFLOW_SERVER", "https://user:secret-value@argo.example.com"},
		{"S3_ENDPOINT", "https://s3.example.com?token=secret-value"},
		{"REGISTRY_URL", "ftp://registry.example.com"},
		{"SIMSCOPE_URL", "https://user:secret-value@viewer.example.com"},
		{"SIMSCOPE_URL", "https://viewer.example.com?token=secret-value"},
		{"SIMSCOPE_URL", "javascript:alert(1)"},
		{"S3_SECRET_ACCESS_KEY", ""}, {"S3_BUCKET", " "},
		{"REGISTRY_USERNAME", "user"}, {"REGISTRY_PASSWORD", "secret-value"},
		{"S3_FORCE_PATH_STYLE", "invalid"}, {"HTTP_TIMEOUT", "0s"}, {"HTTP_TIMEOUT", "-1s"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			values := validEnv()
			values[tc.key] = tc.value
			_, err := Load(lookup(values))
			if err == nil || !strings.Contains(err.Error(), tc.key) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("expected safe validation error for %s, got %v", tc.key, err)
			}
		})
	}
}

func TestRegistryAuthentication(t *testing.T) {
	values := validEnv()
	values["REGISTRY_USERNAME"], values["REGISTRY_PASSWORD"] = "user", "password"
	if _, err := Load(lookup(values)); err != nil {
		t.Fatal(err)
	}
	values["REGISTRY_TOKEN"] = "token"
	if _, err := Load(lookup(values)); err == nil {
		t.Fatal("expected conflicting authentication error")
	}
	delete(values, "REGISTRY_USERNAME")
	delete(values, "REGISTRY_PASSWORD")
	if _, err := Load(lookup(values)); err != nil {
		t.Fatal(err)
	}
}

func TestReadEnvFileAndPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nexport ARGO_WORKFLOW_TOKEN='literal$token#value'\nREGISTRY_PASSWORD=\"password with spaces\" # comment\nS3_BUCKET=workflows # comment\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := ReadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["ARGO_WORKFLOW_TOKEN"] != "literal$token#value" || values["REGISTRY_PASSWORD"] != "password with spaces" || values["S3_BUCKET"] != "workflows" {
		t.Fatal("unexpected literal parsing")
	}
	get := WithFallback(lookup(map[string]string{"ARGO_WORKFLOW_TOKEN": "", "S3_BUCKET": "process-bucket"}), values)
	if value, ok := get("ARGO_WORKFLOW_TOKEN"); !ok || value != "" {
		t.Fatal("explicit empty environment value must override file")
	}
	if value, _ := get("S3_BUCKET"); value != "process-bucket" {
		t.Fatal("process environment must override file")
	}
	if value, _ := get("REGISTRY_PASSWORD"); value != "password with spaces" {
		t.Fatal("missing environment value must fall back to file")
	}
	for _, content := range []string{"invalid", "1INVALID=value", "TOKEN='secret-value", "TOKEN=\"secret-value\" trailing"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadEnvFile(path); err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("expected safe parse error, got %v", err)
		}
	}
}
