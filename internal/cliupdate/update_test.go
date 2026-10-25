package cliupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/google/go-github/v30/github"
)

type fakeSource struct {
	releases  []selfupdate.SourceRelease
	assets    map[int64][]byte
	downloads int
}

func (s *fakeSource) ListReleases(context.Context, selfupdate.Repository) ([]selfupdate.SourceRelease, error) {
	return s.releases, nil
}
func (s *fakeSource) DownloadReleaseAsset(_ context.Context, _ *selfupdate.Release, id int64) (io.ReadCloser, error) {
	s.downloads++
	if b, ok := s.assets[id]; ok {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return nil, errors.New("unavailable")
}

func archive(t *testing.T, platform, content string) []byte {
	t.Helper()
	var b bytes.Buffer
	if platform == "windows" {
		z := zip.NewWriter(&b)
		w, err := z.Create("simworkflow.exe")
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		g := gzip.NewWriter(&b)
		w := tar.NewWriter(g)
		if err := w.WriteHeader(&tar.Header{Name: "simworkflow", Mode: 0755, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
		w.Close()
		g.Close()
	}
	return b.Bytes()
}

func release(t *testing.T, tag, platform, arch string, pre bool, id int64) (selfupdate.SourceRelease, []byte) {
	t.Helper()
	ext := "tar.gz"
	if platform == "windows" {
		ext = "zip"
	}
	name := fmt.Sprintf("simworkflow_%s_%s_%s.%s", tag, platform, arch, ext)
	data := archive(t, platform, "new binary")
	raw, err := json.Marshal(map[string]any{"id": id, "tag_name": tag, "prerelease": pre, "assets": []map[string]any{{"id": id, "name": name, "size": len(data), "browser_download_url": "https://github.com/" + Repository + "/releases/download/" + tag + "/" + name}, {"id": id + 1, "name": "checksums.txt", "browser_download_url": "https://github.com/" + Repository + "/releases/download/" + tag + "/checksums.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	var r github.RepositoryRelease
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return selfupdate.NewGitHubRelease(&r), data
}

func fixture(t *testing.T, platform string) (*Updater, *fakeSource, string) {
	t.Helper()
	r, data := release(t, "v0.2.0", platform, "amd64", true, 1)
	name := r.GetAssets()[0].GetName()
	source := &fakeSource{releases: []selfupdate.SourceRelease{r}, assets: map[int64][]byte{1: data, 2: []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(data), name))}}
	target := filepath.Join(t.TempDir(), "installed")
	if platform == "windows" {
		target += ".exe"
	}
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	u := &Updater{Source: source, OS: platform, Arch: "amd64", Verify: func(_ context.Context, path, version string) error {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "new binary" || version != "v0.2.0" {
			return errors.New("bad candidate")
		}
		return nil
	}}
	return u, source, target
}

func TestUpdateAndFailurePreserveInstalledBinary(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, kind := range []string{"success", "checksum mismatch", "checksum missing line", "missing checksum asset", "unreadable archive", "startup fails", "concurrent installation", "locked"} {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				u, source, target := fixture(t, platform)
				want := "old binary"
				switch kind {
				case "success":
					want = "new binary"
				case "checksum mismatch":
					source.assets[1] = []byte("tampered")
				case "checksum missing line":
					source.assets[2] = []byte(strings.Repeat("a", 64) + "  other.tar.gz\n")
				case "missing checksum asset":
					delete(source.assets, 2)
				case "unreadable archive":
					source.assets[1] = []byte("bad archive")
					source.assets[2] = []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(source.assets[1]), source.releases[0].GetAssets()[0].GetName()))
				case "startup fails":
					u.Verify = func(context.Context, string, string) error { return errors.New("bad executable") }
				case "concurrent installation":
					want = "external update"
					u.Verify = func(context.Context, string, string) error { return os.WriteFile(target, []byte(want), 0755) }
				case "locked":
					if err := os.WriteFile(target+".update.lock", nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				result, err := u.Run(context.Background(), Options{Current: "v0.1.0", Target: target, Prerelease: true}, io.Discard)
				if (err == nil) != (kind == "success") || result.Updated != (kind == "success") {
					t.Fatalf("unexpected update result: %+v, %v", result, err)
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != want {
					t.Fatalf("installed executable: %q %v", data, err)
				}
				files, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".simworkflow-update-*"))
				if err != nil || len(files) != 0 {
					t.Fatal("temporary files were not cleaned")
				}
			})
		}
	}
}

func TestCheckReleasePolicyAndVersionSelection(t *testing.T) {
	u, source, target := fixture(t, "linux")
	stable, data := release(t, "v0.1.5", "linux", "amd64", false, 3)
	source.releases = append(source.releases, stable)
	source.assets[3] = data
	for _, tc := range []struct {
		name, current, version, want string
		pre                          bool
		wantError                    bool
	}{
		{name: "stable only", current: "v0.1.0", want: "v0.1.5"},
		{name: "include prerelease", current: "v0.1.0", pre: true, want: "v0.2.0"},
		{name: "explicit prerelease", current: "v0.1.0", version: "0.2.0", want: "v0.2.0"},
		{name: "development check", current: "dev", pre: true, want: "v0.2.0"},
		{name: "wrong platform", current: "v0.1.0", version: "v9.0.0", wantError: true},
		{name: "invalid version", current: "v0.1.0", version: "not-a-version", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := u.Run(context.Background(), Options{Current: tc.current, Version: tc.version, Check: true, Prerelease: tc.pre, Target: target}, io.Discard)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantError && result.Available != tc.want {
				t.Fatalf("got %s want %s", result.Available, tc.want)
			}
			if result.Updated || source.downloads != 0 {
				t.Fatal("check must not download or install assets")
			}
		})
	}
}

func TestDevelopmentBuildAndDowngradeDoNotModifyFiles(t *testing.T) {
	for _, current := range []string{"dev", "unknown", "v1.0.0"} {
		u, source, target := fixture(t, "linux")
		_, err := u.Run(context.Background(), Options{Current: current, Target: target, Version: "v0.2.0"}, io.Discard)
		if err == nil || source.downloads != 0 {
			t.Fatal("must reject development builds and downgrades before download")
		}
	}
	u, source, target := fixture(t, "linux")
	result, err := u.Run(context.Background(), Options{Current: "v0.2.0", Target: target, Prerelease: true}, io.Discard)
	if err != nil || result.Updated || source.downloads != 0 {
		t.Fatal("current release must not be replaced")
	}
}

func TestUpdateRunningExecutable(t *testing.T) {
	if os.Getenv("SIMWORKFLOW_RUNNING_UPDATE_TEST") == "1" {
		target, err := os.Executable()
		if err != nil || !strings.HasPrefix(filepath.Base(target), "running-update-test") {
			t.Fatal("helper must only update a disposable copied test executable")
		}
		u, _, _ := fixture(t, runtime.GOOS)
		result, err := u.Run(context.Background(), Options{Current: "v0.1.0", Target: target, Prerelease: true}, io.Discard)
		if err != nil || !result.Updated {
			t.Fatalf("running executable replacement failed: %v", err)
		}
		return
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "running-update-test")
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	if err := os.WriteFile(target, data, 0755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(target, "-test.run=^TestUpdateRunningExecutable$")
	command.Env = append(os.Environ(), "SIMWORKFLOW_RUNNING_UPDATE_TEST=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("copied helper failed: %v: %s", err, output)
	}
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "new binary" {
		t.Fatal("running executable was not replaced")
	}
}
