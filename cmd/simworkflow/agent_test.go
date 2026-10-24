package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"simworkflow/internal/agentbuild"
	"simworkflow/internal/config"
	"simworkflow/internal/credentials"
)

func TestAgentBuildCLI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.uv"), []byte("FROM scratch"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	loaded := false
	b := &agentbuild.Builder{Command: func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		if args[0] != "build" {
			t.Fatal("local build must not export")
		}
		if !strings.Contains(strings.Join(args, " "), "--platform linux/amd64") {
			t.Fatal("missing default platform")
		}
		for i, arg := range args {
			if arg == "--iidfile" {
				return os.WriteFile(args[i+1], []byte("sha256:"+strings.Repeat("a", 64)), 0600)
			}
		}
		return errors.New("missing image ID")
	}}
	load := func(push bool) (config.Config, error) {
		loaded = true
		if push {
			t.Fatal("unexpected credentials request")
		}
		return config.Config{Registry: config.Registry{URL: "https://registry.example.com"}}, nil
	}
	err := runAgentWithBuilder([]string{"build", "--repository", "team/agent", "--tag", "v1", "--dockerfile", filepath.Join(dir, "Dockerfile.uv"), dir}, load, b, &out, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded || !strings.Contains(out.String(), "Built registry.example.com/team/agent:v1 locally") || strings.Contains(out.String(), "Building") {
		t.Fatal("incorrect command output")
	}
	for _, args := range [][]string{{"build", "--help"}, {"build"}, {"build", "--repository", "team/agent", "--platform", "linux/amd64,linux/arm64"}, {"build", "--repository", "team/agent", ".", "extra"}} {
		loaded = false
		runAgentWithBuilder(args, load, b, &out, &logs)
		if loaded {
			t.Fatal("help/invalid flags must not read configuration")
		}
	}
}

func TestAgentRegistryOnlyConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, push := range []bool{false, true} {
		backend := &credentialBackend{}
		get := func(k string) (string, bool) {
			v, ok := map[string]string{"CREDENTIALS_STORE": "keyring", "REGISTRY_URL": "https://registry.example.com", "S3_ENDPOINT": "invalid", "ARGO_WORKFLOW_SERVER": "invalid"}[k]
			return v, ok
		}
		args := []string{"--config=", "--env-file=", "agent", "build", "--repository", "team/agent"}
		if push {
			args = append(args, "--push")
		}
		// Missing Dockerfile stops before Docker execution, after configuration.
		var out, logs bytes.Buffer
		err := runWithCredentials(args, get, &out, &logs, &credentials.Store{Backend: backend}, nil)
		if err == nil || !strings.Contains(err.Error(), "Dockerfile") {
			t.Fatalf("unrelated service settings should not be required: %v", err)
		}
		want := 0
		if push {
			want = 1
		}
		if backend.reads != want {
			t.Fatalf("registry-only keyring reads: got %d want %d", backend.reads, want)
		}
	}
}
