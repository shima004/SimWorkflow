package artifacts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"simworkflow/internal/experiment"
	"simworkflow/internal/storage"
)

const testPrefix = "experiments/0123456789abcdef0123456789abcdef"
const firstRun = "run-00000000000000000000000000000001"
const secondRun = "run-00000000000000000000000000000002"

type fakeSource struct {
	objects map[string][]byte
	opened  []string
	broken  string
}

func (s *fakeSource) Open(_ context.Context, key string) (io.ReadCloser, error) {
	s.opened = append(s.opened, key)
	if key == s.broken {
		return nil, errors.New("S3 request failed")
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func tarGZ(t *testing.T, files map[string]string, extra *tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for n, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if extra != nil {
		if err := tw.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func fixture(t *testing.T) (*fakeSource, *experiment.Plan, string) {
	t.Helper()
	source := &fakeSource{objects: map[string][]byte{testPrefix + "/plan.json": []byte(`{"plan":true}`)}}
	for _, id := range []string{firstRun, secondRun} {
		source.objects[testPrefix+"/"+id+"/precompute/data.zip"] = []byte("precompute zip")
		for _, stage := range []string{"precompute", "simulation"} {
			key := testPrefix + "/" + id
			if stage == "precompute" {
				key += "/precompute"
			}
			source.objects[key+"/summary.json"] = []byte(`{"score":5}`)
			source.objects[key+"/bundle.tar.gz"] = tarGZ(t, map[string]string{"logs/" + stage + "/kernel.log": "kernel log", "logs/" + stage + "/rescue.log.7z": "rescue data", "logs/" + stage + "/agent-fire-brigade.log": "agent log", "modules/fireBrigade/module.yaml": "module settings", "scenario/config/kernel.cfg": "kernel settings"}, nil)
		}
	}
	plan := &experiment.Plan{Runs: []experiment.Run{{ID: firstRun}, {ID: secondRun}}}
	return source, plan, filepath.Join(t.TempDir(), "results")
}
func TestSelectedRunStageAndElements(t *testing.T) {
	s, p, out := fixture(t)
	report, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Include: []string{"plan", "score", "simulatorLogs"}, Runs: []string{secondRun}, Stage: "simulation"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Downloaded) != 4 || len(s.opened) != 3 {
		t.Fatalf("files=%d requests=%v", len(report.Downloaded), s.opened)
	}
	for _, file := range report.Downloaded {
		if strings.Contains(file.Path, firstRun) || strings.Contains(file.Path, "agent-") {
			t.Fatal(file)
		}
		body, err := os.ReadFile(filepath.Join(out, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if file.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("wrong checksum")
		}
	}
	if _, err := os.Stat(filepath.Join(out, secondRun, "simulation/logs/rescue.log.7z")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(out, "download.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("manifest must be private")
	}
}
func TestExactFileSkipsOtherStageAndSavesNoOtherEntries(t *testing.T) {
	s, p, out := fixture(t)
	r, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Files: []string{"logs/simulation/rescue.log.7z"}, Runs: []string{firstRun}})
	if err != nil || len(r.Downloaded) != 1 || len(s.opened) != 1 {
		t.Fatalf("%v files=%v requests=%v", err, r.Downloaded, s.opened)
	}
	if r.Downloaded[0].Path != firstRun+"/simulation/logs/rescue.log.7z" {
		t.Fatal(r)
	}
}
func TestAllIncludesRawAndExtractedBundleWithOneFetch(t *testing.T) {
	s, p, out := fixture(t)
	r, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Include: []string{"all"}, Runs: []string{firstRun}, Stage: "simulation"})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, key := range s.opened {
		if strings.HasSuffix(key, "bundle.tar.gz") {
			count++
		}
	}
	if count != 1 {
		t.Fatal(s.opened)
	}
	if len(r.Unavailable) != 1 || !strings.HasSuffix(r.Unavailable[0], "progress.json") {
		t.Fatal(r.Unavailable)
	}
	for _, name := range []string{"plan.json", firstRun + "/simulation/bundle.tar.gz", firstRun + "/simulation/logs/agent-fire-brigade.log", firstRun + "/precompute/data.zip"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestUnavailableSelectionKeepsAvailableFilesAndManifest(t *testing.T) {
	s, p, out := fixture(t)
	delete(s.objects, testPrefix+"/"+firstRun+"/summary.json")
	r, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Include: []string{"plan", "summary"}, Runs: []string{firstRun}, Stage: "simulation"})
	if err == nil || len(r.Unavailable) != 1 || len(r.Downloaded) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	data, e := os.ReadFile(filepath.Join(out, "download.json"))
	if e != nil {
		t.Fatal(e)
	}
	var saved Report
	if e = json.Unmarshal(data, &saved); e != nil || saved.Error == "" {
		t.Fatal("missing partial failure manifest")
	}
}
func TestInvalidSelectionsDoNotCreateOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   Options
		prefix string
	}{
		{name: "unknown include", opts: Options{Include: []string{"typo"}}, prefix: testPrefix},
		{name: "unknown run", opts: Options{Include: []string{"plan"}, Runs: []string{"not-a-run"}}, prefix: testPrefix},
		{name: "file traversal", opts: Options{Files: []string{"../secret"}}, prefix: testPrefix},
		{name: "bad stage", opts: Options{Include: []string{"plan"}, Stage: "other"}, prefix: testPrefix},
		{name: "prefix traversal", opts: Options{Include: []string{"plan"}}, prefix: "experiments/../private"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p, out := fixture(t)
			tc.opts.Output = out
			if _, err := Download(context.Background(), s, p, tc.prefix, tc.opts); err == nil {
				t.Fatal("must reject invalid selection")
			}
			if len(s.opened) != 0 {
				t.Fatal("must validate before transfer")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("must not create output")
			}
		})
	}
}
func TestDoesNotOverwriteExistingOutput(t *testing.T) {
	s, p, out := fixture(t)
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, "plan.json")
	os.WriteFile(path, []byte("existing"), 0600)
	if _, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Include: []string{"plan"}}); err == nil {
		t.Fatal("must reject existing directory")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "existing" || len(s.opened) != 0 {
		t.Fatal("existing file changed")
	}
}
func TestRejectsUnsafeArchivesAndCorruption(t *testing.T) {
	cases := map[string]*tar.Header{
		"traversal": {Name: "../../outside", Typeflag: tar.TypeReg, Mode: 0644},
		"symlink":   {Name: "modules/link", Typeflag: tar.TypeSymlink, Linkname: "/tmp/outside"},
		"hardlink":  {Name: "modules/link", Typeflag: tar.TypeLink, Linkname: "../../outside"},
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			s, p, out := fixture(t)
			s.objects[testPrefix+"/"+firstRun+"/bundle.tar.gz"] = tarGZ(t, map[string]string{}, header)
			if _, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Include: []string{"modules"}, Runs: []string{firstRun}, Stage: "simulation"}); err == nil {
				t.Fatal("must reject unsafe archive")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(out), "outside")); !os.IsNotExist(err) {
				t.Fatal("escaped output directory")
			}
		})
	}
	t.Run("corrupt gzip trailer", func(t *testing.T) {
		s, p, out := fixture(t)
		key := testPrefix + "/" + firstRun + "/bundle.tar.gz"
		body := s.objects[key]
		body[len(body)-8] ^= 1
		s.objects[key] = body
		if _, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Include: []string{"modules"}, Runs: []string{firstRun}, Stage: "simulation"}); err == nil {
			t.Fatal("must reject corrupt bundle")
		}
	})
	t.Run("missing exact file", func(t *testing.T) {
		s, p, out := fixture(t)
		if _, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Files: []string{"logs/simulation/absent.log"}, Runs: []string{firstRun}}); err == nil {
			t.Fatal("missing requested file must fail")
		}
	})
}

type interruptedSource struct{}
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection interrupted") }
func (interruptedSource) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(io.MultiReader(strings.NewReader("partial"), failingReader{})), nil
}
func TestInterruptedFileIsRemoved(t *testing.T) {
	_, p, out := fixture(t)
	r, err := Download(context.Background(), interruptedSource{}, p, testPrefix, Options{Output: out, Include: []string{"plan"}})
	if err == nil || len(r.Downloaded) != 0 {
		t.Fatal("must report transfer failure")
	}
	if _, err := os.Stat(filepath.Join(out, "plan.json")); !os.IsNotExist(err) {
		t.Fatal("partial file was left behind")
	}
	if _, err := os.Stat(filepath.Join(out, "download.json")); err != nil {
		t.Fatal("failure manifest missing")
	}
}
func TestExplicitMissingFileFailsWithAll(t *testing.T) {
	s, p, out := fixture(t)
	_, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Include: []string{"all"}, Files: []string{"logs/simulation/absent.log"}, Stage: "simulation", Runs: []string{firstRun}})
	if err == nil {
		t.Fatal("all must not hide an explicitly missing file")
	}
}
func TestDuplicateArchiveEntriesAreRejected(t *testing.T) {
	s, p, out := fixture(t)
	s.objects[testPrefix+"/"+firstRun+"/bundle.tar.gz"] = tarGZ(t, map[string]string{"modules/fireBrigade/module.yaml": "first"}, &tar.Header{Name: "modules/fireBrigade/module.yaml", Typeflag: tar.TypeReg})
	if _, err := Download(context.Background(), s, p, testPrefix, Options{Output: out, Include: []string{"modules"}, Stage: "simulation", Runs: []string{firstRun}}); err == nil {
		t.Fatal("must reject duplicate archive entries")
	}
}
