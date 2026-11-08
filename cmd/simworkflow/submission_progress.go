package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"

	"simworkflow/internal/operation"
)

func submissionReporter(out io.Writer) func(operation.Event) {
	return submissionReporterWithClock(out, time.Now)
}

func submissionReporterWithClock(out io.Writer, clock func() time.Time) func(operation.Event) {
	report, _ := operationReporter(out, clock)
	return report
}

func operationReporter(out io.Writer, clock func() time.Time) (func(operation.Event), func()) {
	started := clock()
	lastStage := ""
	var lastPrinted time.Time
	tty := false
	if file, ok := out.(*os.File); ok {
		tty = term.IsTerminal(file.Fd()) && os.Getenv("TERM") != "dumb"
	}
	options := []progress.Option{progress.WithDefaultGradient(), progress.WithoutPercentage()}
	if !tty {
		options = append(options, progress.WithColorProfile(termenv.Ascii))
	}
	bar := progress.New(options...)
	openLine := false
	closeLine := func() {
		if openLine {
			fmt.Fprintln(out)
			openLine = false
		}
	}
	return func(event operation.Event) {
		now := clock()
		complete := event.TransferID == 0 && event.Total > 0 && event.Completed == event.Total || event.TransferID != 0 && event.TotalBytes >= 0 && event.Bytes >= event.TotalBytes
		interval := time.Second
		if tty {
			interval = 100 * time.Millisecond
		}
		if event.Stage == lastStage && !complete && now.Sub(lastPrinted) < interval {
			return
		}
		lastStage, lastPrinted = event.Stage, now
		elapsed := now.Sub(started).Truncate(time.Second)
		width := 100
		if tty {
			if w, _, err := term.GetSize(out.(*os.File).Fd()); err == nil && w > 1 {
				width = w
			}
		}
		bar.Width = min(30, max(10, width/4))
		label := strings.ReplaceAll(screenText(event.Stage), "\n", " ")
		line := fmt.Sprintf("[%s] %s", elapsed, label)
		if event.Detail != "" {
			line += ": " + strings.ReplaceAll(screenText(event.Detail), "\n", " ")
		} else if event.TransferID != 0 {
			if event.TotalBytes >= 0 {
				fraction := 1.0
				if event.TotalBytes > 0 {
					fraction = min(1, max(0, float64(event.Bytes)/float64(event.TotalBytes)))
				}
				line += fmt.Sprintf(": %s %.0f%% %s/%s", bar.ViewAs(fraction), fraction*100, downloadBytes(event.Bytes), downloadBytes(event.TotalBytes))
			} else {
				line += ": " + downloadBytes(event.Bytes) + " (size unknown)"
			}
		} else {
			line += fmt.Sprintf(": %d/%d", event.Completed, event.Total)
			if event.Total > 0 {
				line += " " + bar.ViewAs(float64(event.Completed)/float64(event.Total))
			}
		}
		if tty {
			fmt.Fprintf(out, "\r\x1b[2K%s", ansi.Truncate(line, width-1, "…"))
			openLine = true
			if complete || event.Detail != "" {
				closeLine()
			}
		} else {
			fmt.Fprintln(out, line)
		}
	}, closeLine
}
