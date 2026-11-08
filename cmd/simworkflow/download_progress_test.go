package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/muesli/termenv"

	"simworkflow/internal/artifacts"
)

func TestDownloadProgressSizesAndTerminalSafety(t *testing.T) {
	for _, tc := range []struct {
		event artifacts.Progress
		want  []string
	}{
		{artifacts.Progress{Phase: "downloading", Bytes: 1 << 20, TotalBytes: 2 << 20, TotalRuns: 4, CompletedRuns: 1, Path: "logs/\x1b[31mkernel\n.log"}, []string{"█████░░░░░", "50%", "1.0MiB/2.0MiB", "runs 1/4", "1.0MiB/s"}},
		{artifacts.Progress{Phase: "downloading", Bytes: 123, TotalBytes: -1}, []string{"123B", "size unknown"}},
		{artifacts.Progress{Phase: "finished", CompletedRuns: 4, TotalRuns: 4, TransferredBytes: 3 << 30}, []string{"Finished", "██████████", "runs 4/4", "3.0GiB"}},
		{artifacts.Progress{Phase: "extracting", TotalRuns: 1}, []string{"Extracting", "runs 0/1"}},
	} {
		bar := progress.New(progress.WithWidth(10), progress.WithoutPercentage(), progress.WithColorProfile(termenv.Ascii))
		line := downloadProgressLine(tc.event, time.Second, bar)
		for _, want := range tc.want {
			if !strings.Contains(line, want) {
				t.Fatalf("%q missing %q", line, want)
			}
		}
		if strings.ContainsAny(line, "\x1b\r\n") {
			t.Fatalf("remote file names must not inject terminal controls: %q", line)
		}
		if tc.event.TotalBytes < 0 && strings.Contains(line, "%") {
			t.Fatal("unknown sizes must not have a fabricated percentage", line)
		}
	}
}

func TestDownloadProgressThrottlesPipedOutputAndAlwaysReportsFinal(t *testing.T) {
	var out bytes.Buffer
	now := time.Unix(100, 0)
	p := &downloadProgress{out: &out, enabled: true, now: func() time.Time { return now }}
	event := artifacts.Progress{Phase: "downloading", Path: "bundle.tar.gz", TotalBytes: 100}
	p.update(event)
	for i := range 100 {
		event.Bytes = int64(i)
		p.update(event)
	}
	p.update(artifacts.Progress{Phase: "failed", TotalRuns: 4, CompletedRuns: 1})
	p.close()
	if strings.Count(out.String(), "\n") != 2 || !strings.Contains(out.String(), "Failed") || strings.Contains(out.String(), "\x1b") {
		t.Fatal(out.String())
	}
	out.Reset()
	p = newDownloadProgress(&out, false)
	p.update(event)
	p.close()
	if out.Len() != 0 {
		t.Fatal("disabled progress must produce no output", out.String())
	}
}

func TestDownloadProgressClosesTerminalLineExactlyOnce(t *testing.T) {
	var out bytes.Buffer
	p := &downloadProgress{out: &out, enabled: true, tty: true, now: time.Now}
	p.update(artifacts.Progress{Phase: "downloading", TotalBytes: 100})
	p.update(artifacts.Progress{Phase: "finished", CompletedRuns: 1, TotalRuns: 1})
	p.close()
	p.close()
	if strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), "\r\x1b[2K") {
		t.Fatalf("terminal line must be rewritten and closed exactly once: %q", out.String())
	}
}

func TestDownloadProgressShowsExtractionBeforeBlockingLocalWork(t *testing.T) {
	var out bytes.Buffer
	now := time.Unix(100, 0)
	p := &downloadProgress{out: &out, enabled: true, now: func() time.Time { return now }}
	p.update(artifacts.Progress{Phase: "downloading", Path: "bundle.tar.gz", TotalBytes: 100})
	p.update(artifacts.Progress{Phase: "extracting", Path: "bundle.tar.gz"})
	if !strings.Contains(out.String(), "Extracting") {
		t.Fatal("extraction may block without further network reads; show it immediately", out.String())
	}
}
