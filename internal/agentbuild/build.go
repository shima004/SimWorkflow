// Package agentbuild builds user-owned Dockerfiles and publishes images using
// registry credentials held in memory, without Docker login/config mutations.
package agentbuild

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"simworkflow/internal/config"
)

type Options struct {
	Context, Dockerfile, Repository, Tag, Platform string
	Push                                           bool
}

type Result struct{ Image, ImageID, Published string }

type Command func(context.Context, []string, io.Writer, io.Writer) error
type PublishFunc func(context.Context, config.Registry, time.Duration, string, string) (string, error)
type Builder struct {
	Command Command
	Publish PublishFunc
}

func New() *Builder { return &Builder{Command: dockerCommand, Publish: Publish} }

var platformPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+(?:/[a-z0-9]+)?$`)
var imageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (o Options) Validate() error {
	if o.Repository == "" {
		return errors.New("--repository is required")
	}
	if !platformPattern.MatchString(o.Platform) {
		return errors.New("platform must be a single os/architecture[/variant]")
	}
	return nil
}

func (b *Builder) Build(ctx context.Context, o Options, c config.Config, logs io.Writer) (Result, error) {
	if err := o.Validate(); err != nil {
		return Result{}, err
	}
	ref, err := Reference(c.Registry.URL, o.Repository, o.Tag)
	if err != nil {
		return Result{}, err
	}
	contextPath, err := filepath.Abs(o.Context)
	if err != nil {
		return Result{}, errors.New("invalid build context path")
	}
	info, err := os.Stat(contextPath)
	if err != nil || !info.IsDir() {
		return Result{}, errors.New("build context must be an existing local directory")
	}
	dockerfile := o.Dockerfile
	if dockerfile == "" {
		dockerfile = filepath.Join(contextPath, "Dockerfile")
	}
	dockerfile, err = filepath.Abs(dockerfile)
	if err != nil {
		return Result{}, errors.New("invalid Dockerfile path")
	}
	info, err = os.Stat(dockerfile)
	if err != nil || !info.Mode().IsRegular() {
		return Result{}, errors.New("Dockerfile must be an existing regular file")
	}
	temporary, err := os.MkdirTemp("", "simworkflow-agent-build-")
	if err != nil {
		return Result{}, errors.New("could not create private build directory")
	}
	defer os.RemoveAll(temporary)
	idPath := filepath.Join(temporary, "image-id")
	fmt.Fprintf(logs, "Building %s for %s\n", ref.Name(), o.Platform)
	if err := b.Command(ctx, []string{"build", "--load", "--platform", o.Platform, "--file", dockerfile, "--tag", ref.Name(), "--iidfile", idPath, contextPath}, logs, logs); err != nil {
		if ctx.Err() != nil {
			return Result{}, errors.New("agent build cancelled")
		}
		return Result{}, errors.New("Docker build failed; inspect the build output (Docker with Buildx and a running daemon are required)")
	}
	data, err := os.ReadFile(idPath)
	id := strings.TrimSpace(string(data))
	if err != nil || !imageIDPattern.MatchString(id) {
		return Result{}, errors.New("Docker did not produce a valid image ID")
	}
	result := Result{Image: ref.Name(), ImageID: id}
	if !o.Push {
		return result, nil
	}
	archive := filepath.Join(temporary, "image.tar")
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, errors.New("could not create private image archive")
	}
	// Save by immutable local ID so another build cannot change the tag's image
	// between build and push. No registry credentials enter Docker's arguments.
	saveErr := b.Command(ctx, []string{"image", "save", id}, f, logs)
	closeErr := f.Close()
	if saveErr != nil || closeErr != nil {
		return result, errors.New("could not export the built image")
	}
	if err := CheckPlatform(archive, o.Platform); err != nil {
		return result, err
	}
	fmt.Fprintf(logs, "Uploading %s\n", ref.Name())
	result.Published, err = b.Publish(ctx, c.Registry, c.HTTPTimeout, archive, ref.Name())
	return result, err
}

func dockerCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnvironment(os.Environ())
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

func dockerEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "ARGO_WORKFLOW_TOKEN", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_SESSION_TOKEN", "REGISTRY_USERNAME", "REGISTRY_PASSWORD", "REGISTRY_TOKEN":
			continue
		}
		result = append(result, entry)
	}
	return result
}
