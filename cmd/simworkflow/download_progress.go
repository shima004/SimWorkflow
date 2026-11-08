package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"

	"simworkflow/internal/artifacts"
)

type downloadProgress struct {
	out             io.Writer
	enabled, tty    bool
	closed, printed bool
	lastPrint       time.Time
	fileStarted     time.Time
	path            string
	filePhase       string
	lastPhase       string
	bar             *progress.Model
	now             func() time.Time
}

func newDownloadProgress(out io.Writer, enabled bool) *downloadProgress {
	p := &downloadProgress{out: out, enabled: enabled, now: time.Now}
	if f, ok := out.(*os.File); ok {
		p.tty = term.IsTerminal(f.Fd()) && os.Getenv("TERM") != "dumb"
	}
	p.update(artifacts.Progress{Phase: "preparing", TotalBytes: -1})
	return p
}

func (p *downloadProgress) update(event artifacts.Progress) {
	if !p.enabled || p.closed {
		return
	}
	now := p.now()
	fileProgress := event.Phase == "downloading" || (event.Phase == "extracting" && event.TotalBytes >= 0)
	if fileProgress && (p.path != event.Path || p.filePhase != event.Phase) {
		p.path, p.filePhase, p.fileStarted = event.Path, event.Phase, now
	}
	final := event.Phase == "finished" || event.Phase == "failed"
	transition := (event.Phase == "extracting" && p.lastPhase != "extracting") ||
		((event.Phase == "downloading" || event.Phase == "indexing") && p.lastPhase == "preparing")
	interval := time.Second
	if p.tty {
		interval = 100 * time.Millisecond
	}
	if !final && !transition && !p.lastPrint.IsZero() && now.Sub(p.lastPrint) < interval {
		return
	}
	if p.tty && strings.HasPrefix(event.Path, "run-") {
		if _, suffix, ok := strings.Cut(event.Path, "/"); ok {
			event.Path = suffix
		}
	}
	width := 100
	if p.tty {
		if f, ok := p.out.(*os.File); ok {
			if w, _, err := term.GetSize(f.Fd()); err == nil && w > 1 {
				width = w
			}
		}
	}
	if p.bar == nil {
		bar := progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage())
		if !p.tty {
			bar = progress.New(progress.WithoutPercentage(), progress.WithColorProfile(termenv.Ascii))
		}
		p.bar = &bar
	}
	p.bar.Width = min(30, max(10, width/4))
	line := downloadProgressLine(event, now.Sub(p.fileStarted), *p.bar)
	if p.tty {
		fmt.Fprintf(p.out, "\r\x1b[2K%s", ansi.Truncate(line, width-1, "…"))
	} else {
		fmt.Fprintln(p.out, line)
	}
	p.lastPrint, p.printed = now, true
	p.lastPhase = event.Phase
}

func (p *downloadProgress) close() {
	if p.closed {
		return
	}
	if p.enabled && p.tty && p.printed {
		fmt.Fprintln(p.out)
	}
	p.closed = true
}

func downloadProgressLine(event artifacts.Progress, elapsed time.Duration, bar progress.Model) string {
	phase := map[string]string{"preparing": "Preparing", "checking": "Checking", "indexing": "Indexing ZIP", "downloading": "Downloading", "extracting": "Extracting", "run-complete": "Processed", "finished": "Finished", "failed": "Failed"}[event.Phase]
	if phase == "" {
		phase = "Preparing"
	}
	runs := ""
	if event.TotalRuns > 0 {
		runs = fmt.Sprintf(" | runs %d/%d", event.CompletedRuns, event.TotalRuns)
	}
	if event.Phase == "downloading" || (event.Phase == "extracting" && event.TotalBytes >= 0) {
		var transfer string
		if event.TotalBytes >= 0 {
			fraction := float64(1)
			if event.TotalBytes > 0 {
				fraction = min(1, max(0, float64(event.Bytes)/float64(event.TotalBytes)))
			}
			transfer = fmt.Sprintf("%s %3.0f%% %s/%s", bar.ViewAs(fraction), math.Floor(fraction*100), downloadBytes(event.Bytes), downloadBytes(event.TotalBytes))
		} else {
			transfer = fmt.Sprintf("[    ?     ] %s (size unknown)", downloadBytes(event.Bytes))
		}
		speed := ""
		if event.Bytes > 0 && elapsed > 0 {
			speed = fmt.Sprintf(" | %s/s", downloadBytes(int64(float64(event.Bytes)/elapsed.Seconds())))
		}
		return phase + " " + transfer + runs + speed + " | " + strings.ReplaceAll(screenText(event.Path), "\n", " ")
	}
	barView := ""
	if event.TotalRuns > 0 {
		barView = " " + bar.ViewAs(float64(event.CompletedRuns)/float64(event.TotalRuns))
	}
	path := strings.ReplaceAll(screenText(event.Path), "\n", " ")
	if path != "" {
		path = " | " + path
	}
	return fmt.Sprintf("%s%s%s | transferred %s%s", phase, barView, runs, downloadBytes(event.TransferredBytes), path)
}

func downloadBytes(n int64) string {
	for _, unit := range []struct {
		size int64
		name string
	}{{1 << 40, "TiB"}, {1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if n >= unit.size {
			return fmt.Sprintf("%.1f%s", float64(n)/float64(unit.size), unit.name)
		}
	}
	return fmt.Sprintf("%dB", n)
}
