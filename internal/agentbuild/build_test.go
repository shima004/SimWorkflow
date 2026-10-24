package agentbuild

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"simworkflow/internal/config"
)

func testArchive(t *testing.T) string {
	t.Helper()
	c, _ := empty.Image.ConfigFile()
	c.OS, c.Architecture = "linux", "amd64"
	image, err := mutate.ConfigFile(empty.Image, c)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := name.NewTag("registry.example.com/test/agent:v1")
	path := filepath.Join(t.TempDir(), "image.tar")
	if err := tarball.WriteToFile(path, ref, image); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuildExportAndCleanup(t *testing.T) {
	for _, push := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "push"}[push], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0600); err != nil {
				t.Fatal(err)
			}
			archive := testArchive(t)
			id := "sha256:" + strings.Repeat("a", 64)
			var commands [][]string
			var temporary string
			published := false
			b := &Builder{Command: func(ctx context.Context, args []string, out, errOut io.Writer) error {
				commands = append(commands, args)
				if args[0] == "build" {
					for i, arg := range args {
						if arg == "--iidfile" {
							temporary = filepath.Dir(args[i+1])
							return os.WriteFile(args[i+1], []byte(id), 0600)
						}
					}
					return errors.New("missing iidfile")
				}
				if !reflect.DeepEqual(args, []string{"image", "save", id}) {
					t.Fatal("must export by immutable local ID")
				}
				f, err := os.Open(archive)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(out, f)
				return err
			}, Publish: func(ctx context.Context, c config.Registry, timeout time.Duration, path, ref string) (string, error) {
				published = true
				if timeout != 17*time.Second || c.Password != "secret" || ref != "registry.example.com/team/agent:v1" {
					t.Fatal("incorrect publication settings")
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0600 {
					t.Fatal("archive must be private")
				}
				return "registry.example.com/team/agent@" + id, nil
			}}
			c := config.Config{Registry: config.Registry{URL: "https://registry.example.com", Username: "user", Password: "secret"}, HTTPTimeout: 17 * time.Second}
			result, err := b.Build(context.Background(), Options{Context: dir, Repository: "team/agent", Tag: "v1", Platform: "linux/amd64", Push: push}, c, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if result.Image != "registry.example.com/team/agent:v1" || published != push || (result.Published != "") != push {
				t.Fatal("incorrect result")
			}
			if _, err := os.Stat(temporary); !os.IsNotExist(err) {
				t.Fatal("temporary archive must be removed")
			}
			want := []string{"build", "--load", "--platform", "linux/amd64", "--file", filepath.Join(dir, "Dockerfile"), "--tag", result.Image, "--iidfile", filepath.Join(temporary, "image-id"), dir}
			if !reflect.DeepEqual(commands[0], want) || len(commands) != map[bool]int{false: 1, true: 2}[push] {
				t.Fatalf("unexpected Docker commands: %v", commands)
			}
		})
	}
}

func TestBuildFailureDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &Builder{Command: func(context.Context, []string, io.Writer, io.Writer) error { return errors.New("private-secret") }, Publish: func(context.Context, config.Registry, time.Duration, string, string) (string, error) {
		t.Fatal("must not publish")
		return "", nil
	}}
	for _, ctx := range []context.Context{context.Background(), ctx} {
		_, err := b.Build(ctx, Options{Context: dir, Repository: "team/agent", Tag: "v1", Platform: "linux/amd64", Push: true}, config.Config{Registry: config.Registry{URL: "https://registry.example.com"}}, io.Discard)
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("expected sanitized failure")
		}
	}
}

func TestCheckPlatform(t *testing.T) {
	path := testArchive(t)
	if err := CheckPlatform(path, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"linux/arm64", "windows/amd64", "linux/amd64/v3"} {
		if err := CheckPlatform(path, p); err == nil {
			t.Fatalf("accepted mismatched platform %s", p)
		}
	}
}

func TestDockerEnvironment(t *testing.T) {
	input := []string{"PATH=/bin", "DOCKER_HOST=unix:///run/docker.sock", "HTTP_PROXY=http://proxy.example.com", "ARGO_WORKFLOW_TOKEN=secret", "S3_ACCESS_KEY_ID=secret", "S3_SECRET_ACCESS_KEY=secret", "S3_SESSION_TOKEN=secret", "REGISTRY_USERNAME=secret", "REGISTRY_PASSWORD=secret", "registry_token=secret"}
	want := input[:3]
	if got := dockerEnvironment(input); !reflect.DeepEqual(got, want) {
		t.Fatal("Docker must not inherit SimWorkflow service credentials")
	}
}
