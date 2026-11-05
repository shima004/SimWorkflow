package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"simworkflow/internal/storage"
)

type zipSource struct {
	*fakeSource
	readBytes   int
	unsupported bool
}

func (s *zipSource) OpenReaderAt(_ context.Context, key string) (io.ReaderAt, int64, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, 0, storage.ErrNotFound
	}
	if s.unsupported {
		return nil, int64(len(data)), storage.ErrRangeUnsupported
	}
	return zipReaderAt{bytes.NewReader(data), s}, int64(len(data)), nil
}

type zipReaderAt struct {
	*bytes.Reader
	source *zipSource
}

func (r zipReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.Reader.ReadAt(p, off)
	r.source.readBytes += n
	return n, err
}

func zipResults(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, item := range []struct{ name, body string }{
		{"logs/simulation/kernel.log", strings.Repeat("x", 8<<20)},
		{"logs/simulation/score.log", "score: 123\n"},
		{"logs/simulation/agent-fire-brigade.log", "agent log\n"},
		{"modules/fireBrigade/module.yaml", "Module: example.A\n"},
	} {
		w, err := z.CreateHeader(&zip.FileHeader{Name: item.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, item.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestZIPFileSelectionOnlyTransfersRequiredRanges(t *testing.T) {
	base, plan, output := fixture(t)
	key := testPrefix + "/" + firstRun + "/bundle.zip"
	base.objects[key] = zipResults(t)
	source := &zipSource{fakeSource: base}
	var last Progress
	options := Options{Output: output, Runs: []string{firstRun}, Stage: "simulation", Files: []string{"logs/simulation/score.log"}, Progress: func(p Progress) { last = p }}
	report, err := Download(context.Background(), source, plan, testPrefix, options)
	if err != nil || report.Added != 1 {
		t.Fatal(err, report)
	}
	data, err := os.ReadFile(filepath.Join(output, firstRun, "simulation/logs/score.log"))
	if err != nil || string(data) != "score: 123\n" || source.readBytes >= 1<<20 || len(source.opened) != 0 {
		t.Fatalf("err=%v data=%q range-bytes=%d full-gets=%v", err, data, source.readBytes, source.opened)
	}
	if report.Downloaded[0].Source != key+"#logs/simulation/score.log" || last.TransferredBytes != int64(source.readBytes) || last.Phase != "finished" {
		t.Fatal("wrong provenance or range progress", report.Downloaded, last)
	}
	report, err = Download(context.Background(), source, plan, testPrefix, options)
	if err != nil || report.Added != 0 || len(report.Skipped) != 1 {
		t.Fatal("verified ZIP selections must remain skippable", err, report)
	}
}

func TestZIPCategorySelectionDoesNotReadKernel(t *testing.T) {
	base, plan, output := fixture(t)
	base.objects[testPrefix+"/"+firstRun+"/bundle.zip"] = zipResults(t)
	source := &zipSource{fakeSource: base}
	report, err := Download(context.Background(), source, plan, testPrefix, Options{Output: output, Runs: []string{firstRun}, Stage: "simulation", Include: []string{"agentLogs"}})
	if err != nil || report.Added != 1 || source.readBytes >= 1<<20 || len(source.opened) != 0 || !strings.HasSuffix(report.Downloaded[0].Path, "agent-fire-brigade.log") {
		t.Fatalf("err=%v report=%+v range-bytes=%d full-gets=%v", err, report, source.readBytes, source.opened)
	}
}

func TestZIPFullBundleAndRangeFallback(t *testing.T) {
	for _, mode := range []string{"unsupported-range", "raw-bundle", "bundle-and-extracted"} {
		t.Run(mode, func(t *testing.T) {
			base, plan, output := fixture(t)
			key := testPrefix + "/" + firstRun + "/bundle.zip"
			base.objects[key] = zipResults(t)
			source := &zipSource{fakeSource: base, unsupported: mode == "unsupported-range"}
			options := Options{Output: output, Runs: []string{firstRun}, Stage: "simulation"}
			want := 1
			if mode == "unsupported-range" {
				options.Files = []string{"logs/simulation/score.log"}
			} else {
				options.Include = []string{"bundle"}
				if mode == "bundle-and-extracted" {
					options.Files = []string{"logs/simulation/score.log"}
					want = 2
				}
			}
			report, err := Download(context.Background(), source, plan, testPrefix, options)
			if err != nil || report.Added != want || len(source.opened) != 1 || source.opened[0] != key {
				t.Fatalf("err=%v report=%+v requests=%v", err, report, source.opened)
			}
		})
	}
}

func TestLegacyTarFallbackOnlyWhenZIPIsAbsent(t *testing.T) {
	base, plan, output := fixture(t)
	source := &zipSource{fakeSource: base}
	report, err := Download(context.Background(), source, plan, testPrefix, Options{Output: output, Runs: []string{firstRun}, Stage: "simulation", Include: []string{"agentLogs"}})
	if err != nil || report.Added != 1 || len(source.opened) != 1 || !strings.HasSuffix(source.opened[0], "bundle.tar.gz") {
		t.Fatal(err, report, source.opened)
	}
	base.objects[testPrefix+"/"+firstRun+"/bundle.zip"] = []byte("corrupt ZIP")
	source.opened = nil
	_, err = Download(context.Background(), source, plan, testPrefix, Options{Output: filepath.Join(t.TempDir(), "bad"), Runs: []string{firstRun}, Stage: "simulation", Include: []string{"agentLogs"}})
	if err == nil || len(source.opened) != 0 || errors.Is(err, storage.ErrNotFound) {
		t.Fatal("corrupt ZIP must not silently use old results", err, source.opened)
	}
}
