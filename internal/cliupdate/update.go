package cliupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/creativeprojects/go-selfupdate/update"
)

type Options struct {
	Current, Version, Target string
	Check, Prerelease        bool
}
type Result struct {
	Current, Available string
	Updated            bool
}
type Updater struct {
	Source   selfupdate.Source
	OS, Arch string
	Verify   func(context.Context, string, string) error
}

func New() *Updater {
	return &Updater{Source: NewPublicSource(), OS: runtime.GOOS, Arch: runtime.GOARCH, Verify: verifyBinary}
}

func (u *Updater) Run(ctx context.Context, o Options, log io.Writer) (Result, error) {
	result := Result{Current: o.Current}
	current, currentErr := semver.StrictNewVersion(strings.TrimPrefix(o.Current, "v"))
	if currentErr != nil && !o.Check {
		return result, errors.New("self-update requires a distributed versioned binary; go run and development builds cannot update themselves")
	}
	if o.Version != "" {
		v, err := semver.StrictNewVersion(strings.TrimPrefix(o.Version, "v"))
		if err != nil {
			return result, errors.New("--version must be a semantic version such as v0.1.2")
		}
		o.Version = "v" + v.String()
	}
	extension := "tar.gz"
	if u.OS == "windows" {
		extension = "zip"
	}
	filter := fmt.Sprintf(`^simworkflow_v[^/]+_%s_%s\.%s$`, regexp.QuoteMeta(u.OS), regexp.QuoteMeta(u.Arch), regexp.QuoteMeta(extension))
	detector, err := selfupdate.NewUpdater(selfupdate.Config{Source: u.Source, OS: u.OS, Arch: u.Arch, Prerelease: o.Prerelease, Filters: []string{filter}, Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"}})
	if err != nil {
		return result, errors.New("could not initialize self-update")
	}
	release, found, err := detector.DetectVersion(ctx, selfupdate.ParseSlug(Repository), o.Version)
	if err != nil {
		return result, errors.New("could not find a validated release; check GitHub connectivity and release checksums")
	}
	if !found {
		return result, errors.New("no compatible release found; use --prerelease for v0.x releases or specify --version")
	}
	result.Available = "v" + release.Version()
	expected := fmt.Sprintf("simworkflow_%s_%s_%s.%s", result.Available, u.OS, u.Arch, extension)
	if release.AssetName != expected {
		return result, errors.New("release asset does not match the selected version and platform")
	}
	if o.Check {
		return result, nil
	}
	if current != nil && !release.GreaterThan(current.String()) {
		if o.Version != "" && !release.Equal(current.String()) {
			return result, errors.New("refusing to downgrade the installed CLI")
		}
		return result, nil
	}
	info, err := os.Stat(o.Target)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("could not locate the installed executable")
	}
	lock, err := os.OpenFile(o.Target+".update.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, errors.New("cannot lock installation directory; check write permission or another self-update")
	}
	lock.Close()
	defer os.Remove(o.Target + ".update.lock")
	original, err := fileHash(o.Target)
	if err != nil {
		return result, errors.New("could not read installed executable")
	}
	directory, err := os.MkdirTemp(filepath.Dir(o.Target), ".simworkflow-update-")
	if err != nil {
		return result, errors.New("installation directory is not writable")
	}
	defer os.RemoveAll(directory)
	binary := filepath.Join(directory, "simworkflow")
	if u.OS == "windows" {
		binary += ".exe"
	}
	if err := os.WriteFile(binary, nil, 0700); err != nil {
		return result, errors.New("could not create temporary executable")
	}
	fmt.Fprintf(log, "Downloading %s and verifying SHA-256...\n", release.AssetName)
	if err := detector.UpdateTo(ctx, release, binary); err != nil {
		return result, errors.New("release download, checksum verification or extraction failed; installed CLI was preserved")
	}
	if err := u.Verify(ctx, binary, result.Available); err != nil {
		return result, errors.New("new CLI failed its version/startup check; installed CLI was preserved")
	}
	after, err := fileHash(o.Target)
	if err != nil || after != original {
		return result, errors.New("installed executable changed during update; refusing to overwrite it")
	}
	if ctx.Err() != nil {
		return result, errors.New("self-update cancelled; installed CLI was preserved")
	}
	file, err := os.Open(binary)
	if err != nil {
		return result, errors.New("could not read verified executable")
	}
	defer file.Close()
	err = update.Apply(file, update.Options{TargetPath: o.Target, TargetMode: info.Mode().Perm()})
	if err != nil {
		if update.RollbackError(err) != nil {
			return result, fmt.Errorf("update rollback failed; recover the executable from %s", filepath.Join(filepath.Dir(o.Target), "."+filepath.Base(o.Target)+".old"))
		}
		return result, errors.New("could not replace executable; installed CLI was preserved")
	}
	result.Updated = true
	return result, nil
}

func fileHash(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var value [32]byte
	copy(value[:], h.Sum(nil))
	return value, nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 4096 {
		b.Buffer.Write(p[:min(n, 4096-b.Len())])
	}
	return n, nil
}
func verifyBinary(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "--version")
	command.Dir = filepath.Dir(path)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "ARGO_WORKFLOW_TOKEN", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_SESSION_TOKEN", "REGISTRY_USERNAME", "REGISTRY_PASSWORD", "REGISTRY_TOKEN", "GITHUB_TOKEN":
			continue
		}
		command.Env = append(command.Env, entry)
	}
	var output limitedOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return errors.New("startup failed")
	}
	line := strings.TrimSpace(output.String())
	if !strings.HasPrefix(line, "simworkflow "+version+" (commit ") || !strings.HasSuffix(line, ")") {
		return errors.New("version mismatch")
	}
	return nil
}
