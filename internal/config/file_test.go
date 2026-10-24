package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestYAMLSettingsAndEnvironmentOverrides(t *testing.T) {
	values, err := parseYAML([]byte(`credentials:
  store: environment
argoWorkflow:
  server: https://argo.example.com
  namespace: my-namespace
  token: file-token
s3:
  endpoint: https://s3.example.com
  region: ap-northeast-1
  bucket: results
  accessKeyID: file-key
  secretAccessKey: file-secret
  sessionToken: file-session
  forcePathStyle: false
registry:
  url: https://ghcr.io
  username: ""
  password: ""
  token: file-registry-token
simScope:
  url: https://viewer.example.com
httpTimeout: 12s
`))
	if err != nil {
		t.Fatal(err)
	}
	if values["CREDENTIALS_STORE"] != "environment" {
		t.Fatal("credential store mode must be mapped from YAML")
	}
	c, err := Load(lookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if c.Argo.Namespace != "my-namespace" || c.Argo.Token != "file-token" || c.S3.Region != "ap-northeast-1" || c.S3.Bucket != "results" || c.S3.AccessKeyID != "file-key" || c.S3.SecretAccessKey != "file-secret" || c.S3.SessionToken != "file-session" || c.S3.ForcePathStyle || c.Registry.Token != "file-registry-token" || c.SimScopeURL != "https://viewer.example.com" || c.HTTPTimeout != 12*time.Second {
		t.Fatal("YAML fields were not mapped to the existing configuration schema")
	}
	c, err = Load(WithFallback(lookup(map[string]string{"ARGO_WORKFLOW_TOKEN": "", "S3_FORCE_PATH_STYLE": "true", "HTTP_TIMEOUT": "17s"}), values))
	if err != nil || c.Argo.Token != "" || !c.S3.ForcePathStyle || c.HTTPTimeout != 17*time.Second {
		t.Fatal("explicit environment values must override YAML, including empty values")
	}
	partial, err := parseYAML([]byte("argoWorkflow:\n  token: \"\"\ns3:\n  forcePathStyle: false\n"))
	if err != nil || len(partial) != 2 || partial["ARGO_WORKFLOW_TOKEN"] != "" || partial["S3_FORCE_PATH_STYLE"] != "false" {
		t.Fatal("omitted fields must remain absent and explicit empty/false values must be retained")
	}
}

func TestInvalidYAMLDoesNotExposeCredentials(t *testing.T) {
	for _, data := range []string{
		"private-secret: value\n",
		"argoWorkflow:\n  unknown: private-secret\n",
		"s3:\n  forcePathStyle: private-secret\n",
		"argoWorkflow:\n  token: [private-secret]\n",
		"argoWorkflow:\n  token: private-secret\n  token: second\n",
		"argoWorkflow:\n  token: 'private-secret\n",
		"{}\n---\nprivate-secret: value\n",
	} {
		if _, err := parseYAML([]byte(data)); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("invalid YAML must fail without exposing values")
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 1<<20)+"{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadYAML(path); err == nil {
		t.Fatal("oversized configuration must fail")
	}
}

func TestDefaultConfigPathAndPrivateInitialization(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := DefaultPath()
	if err != nil || path != filepath.Join(root, "simworkflow", "config.yaml") {
		t.Fatal("default path must use XDG_CONFIG_HOME")
	}
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("initialized configuration must be private")
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || dir.Mode().Perm() != 0700 {
		t.Fatal("new configuration directory must be private")
	}
	values, err := ReadYAML(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ARGO_WORKFLOW_TOKEN", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_SESSION_TOKEN", "REGISTRY_PASSWORD", "REGISTRY_TOKEN"} {
		if _, ok := values[key]; ok {
			t.Fatal("template must not contain credential fields")
		}
	}
	if values["CREDENTIALS_STORE"] != "keyring" {
		t.Fatal("new configurations must use the OS keyring")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(path); !errors.Is(err, os.ErrExist) {
		t.Fatal("initialization must not overwrite an existing file")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("existing configuration changed")
	}
	t.Setenv("XDG_CONFIG_HOME", "relative-directory")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("configuration must not silently depend on the working directory")
	}
}
