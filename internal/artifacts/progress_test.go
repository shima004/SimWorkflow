package artifacts

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type sizedProgressSource struct{ *fakeSource }

func (s sizedProgressSource) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := s.fakeSource.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	return &sizedProgressBody{ReadCloser: r, size: int64(len(s.objects[key]))}, nil
}

type sizedProgressBody struct {
	io.ReadCloser
	size int64
}

func (r *sizedProgressBody) Size() int64 { return r.size }
func (r *sizedProgressBody) Read(p []byte) (int, error) {
	return r.ReadCloser.Read(p[:min(len(p), 8)])
}

func TestDownloadProgressCountsTransferOnceBeforeExtraction(t *testing.T) {
	for _, sized := range []bool{true, false} {
		t.Run(map[bool]string{true: "known-size", false: "unknown-size"}[sized], func(t *testing.T) {
			s, plan, output := fixture(t)
			var source Source = s
			if sized {
				source = sizedProgressSource{s}
			}
			var events []Progress
			_, err := Download(context.Background(), source, plan, testPrefix, Options{Output: output, Runs: []string{firstRun}, Stage: "simulation", Include: []string{"bundle", "simulatorLogs"}, Progress: func(p Progress) { events = append(events, p) }})
			if err != nil {
				t.Fatal(err)
			}
			size := int64(len(s.objects[testPrefix+"/"+firstRun+"/bundle.tar.gz"]))
			last := events[len(events)-1]
			if last.Phase != "finished" || last.TotalRuns != 1 || last.CompletedRuns != 1 || last.TransferredBytes != size || len(s.opened) != 1 {
				t.Fatalf("wrong final progress or duplicate transfer: %+v requests=%v", last, s.opened)
			}
			partial, extracted := false, false
			var previous int64
			for _, event := range events {
				if event.TransferredBytes < previous {
					t.Fatal("transferred bytes went backwards")
				}
				previous = event.TransferredBytes
				if event.Phase == "downloading" {
					want := int64(-1)
					if sized {
						want = size
					}
					if event.Bytes > 0 && event.TotalBytes != want {
						t.Fatal("incorrect or invented transfer size", event)
					}
					partial = partial || (event.Bytes > 0 && event.Bytes < size)
				}
				if event.Phase == "extracting" {
					extracted = true
					if event.TransferredBytes != size {
						t.Fatal("extracting was reported before the bundle was transferred", event)
					}
				}
			}
			if !extracted || (sized && !partial) {
				t.Fatal("must report during transfer and before extraction")
			}
		})
	}
}

func TestSkippedDownloadReportsNoNetworkTransfer(t *testing.T) {
	s, plan, output := fixture(t)
	opts := Options{Output: output, Runs: []string{firstRun}, Stage: "simulation", Include: []string{"summary"}}
	if _, err := Download(context.Background(), s, plan, testPrefix, opts); err != nil {
		t.Fatal(err)
	}
	s.opened = nil
	var last Progress
	opts.Progress = func(p Progress) { last = p }
	r, err := Download(context.Background(), s, plan, testPrefix, opts)
	if err != nil || len(r.Skipped) != 1 || len(s.opened) != 0 || last.TransferredBytes != 0 || last.CompletedRuns != 1 || last.Phase != "finished" {
		t.Fatalf("err=%v report=%+v progress=%+v requests=%v", err, r, last, s.opened)
	}
}

type interruptedProgressSource struct{}

func (interruptedProgressSource) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(interruptedProgressBody{}), nil
}

type interruptedProgressBody struct{}

func (interruptedProgressBody) Read(p []byte) (int, error) {
	return copy(p, "partial"), errors.New("connection interrupted")
}

func TestInterruptedDownloadDoesNotReportCompletion(t *testing.T) {
	_, plan, output := fixture(t)
	var last Progress
	_, err := Download(context.Background(), interruptedProgressSource{}, plan, testPrefix, Options{Output: output, Runs: []string{firstRun}, Stage: "simulation", Include: []string{"summary"}, Progress: func(p Progress) { last = p }})
	if err == nil || last.Phase != "failed" || last.CompletedRuns != 0 || last.TransferredBytes != 7 {
		t.Fatalf("err=%v progress=%+v", err, last)
	}
	if _, err := os.Stat(filepath.Join(output, firstRun, "simulation/summary.json")); !os.IsNotExist(err) {
		t.Fatal("partial files must still be removed", err)
	}
}
